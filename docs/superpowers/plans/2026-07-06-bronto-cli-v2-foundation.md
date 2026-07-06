# bronto-cli v2 — Plan 1: Foundation & Walking Skeleton

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A working `bronto` binary that resolves config/profiles, authenticates against the Bronto API, and can hit any endpoint via `bronto api`, `bronto ping`, and `bronto config` — with the generated OpenAPI client, output engine, and typed-error system in place for later plans.

**Architecture:** Three layers per the spec (`docs/superpowers/specs/2026-07-06-bronto-cli-v2-design.md` §4): Cobra command layer → service layer (minimal in this plan) → generated oapi-codegen client + hand-written transport. One output engine, one config resolver, one typed-error system used by everything.

**Tech Stack:** Go ≥1.24, Cobra, oapi-codegen v2 (generated client checked in), pelletier/go-toml/v2, mattn/go-isatty, rogpeppe/go-internal (testscript).

**Later plans (not here):** search/tail/fields/context + time-range parsing (Plan 2), traces (Plan 3), send + keychain auth (Plan 4), generated CRUD tail + plugins + completions + `--jq`/`--json <fields>` selection (Plan 5), goreleaser/containers/spec-sync CI + skill.md (Plan 6).

## Global Constraints

- Module path: `github.com/svrnm/bronto-cli`; binary name `bronto`; license MIT (as v1).
- Go ≥ 1.24; `CGO_ENABLED=0 go build ./...` must always succeed (static binary requirement).
- Allowed runtime deps ONLY: `github.com/spf13/cobra`, `github.com/pelletier/go-toml/v2`, `github.com/mattn/go-isatty`, `github.com/oapi-codegen/runtime`. Test-only: `github.com/rogpeppe/go-internal`. Anything else needs explicit approval.
- stdout carries data only; all messages/progress/errors go to stderr.
- Exit codes: `0` success · `1` API error · `2` usage/config · `3` auth · `4` not found · `5` rate-limit/timeout.
- Error codes are stable snake_case strings (e.g. `auth_invalid_key`); they are public API.
- Machine-mode error shape on stderr: `{"error":{"code":"...","message":"...","retryable":false}}`.
- Env vars: `BRONTO_API_KEY`, `BRONTO_PROFILE`, `BRONTO_REGION`, `BRONTO_CONFIG_DIR`. Precedence: flags > env > project `.bronto.toml` > user config > defaults.
- Secrets are NEVER read from project `.bronto.toml` and never written to any config file.
- Generated code (`internal/api/gen.go`) is checked in and only changed via `make generate`.
- No telemetry. No network calls except to the configured Bronto API.
- Commit style: conventional commits (`feat: …`, `test: …`, `chore: …`).
- Default region when nothing is configured: `eu` (v1 behavior).

---

### Task 1: Repo scaffold, root command, version, testscript harness

**Files:**
- Create: `go.mod`, `.gitignore`, `LICENSE` (MIT), `Makefile`
- Create: `cmd/bronto/main.go`, `cmd/bronto/main_test.go`
- Create: `internal/version/version.go`, `internal/version/version_test.go`
- Create: `internal/cli/root.go`
- Create: `cmd/bronto/testdata/script/version.txtar`

**Interfaces:**
- Produces: `cli.NewRootCmd() *cobra.Command`; `run() int` in package main (used by testscript); `version.Version/Commit/Date string` vars (set via ldflags), `version.String() string`.

- [ ] **Step 1: Initialize module and deps**

```bash
git checkout -b feat/foundation
go mod init github.com/svrnm/bronto-cli
go get github.com/spf13/cobra@latest
go get github.com/rogpeppe/go-internal@latest
```

Create `.gitignore`:

```gitignore
/bronto
/dist/
*.test
.env
```

Create `LICENSE` with the standard MIT license text, copyright `2026 bronto-cli contributors`.

Create `Makefile`:

```make
.PHONY: build test lint generate check-generate

build:
	CGO_ENABLED=0 go build -o bronto ./cmd/bronto

test:
	go test ./...

lint:
	golangci-lint run

generate:
	go generate ./...

check-generate: generate
	git diff --exit-code -- internal/api api
```

- [ ] **Step 2: Write failing version test**

`internal/version/version_test.go`:

```go
package version

import (
	"strings"
	"testing"
)

func TestStringContainsVersionAndCommit(t *testing.T) {
	got := String()
	if !strings.Contains(got, Version) || !strings.Contains(got, Commit) {
		t.Fatalf("String() = %q, want it to contain %q and %q", got, Version, Commit)
	}
	if !strings.HasPrefix(got, "bronto ") {
		t.Fatalf("String() = %q, want prefix \"bronto \"", got)
	}
}
```

Run: `go test ./internal/version -v` — Expected: FAIL (package/function undefined).

- [ ] **Step 3: Implement version package**

`internal/version/version.go`:

```go
// Package version holds build metadata injected via -ldflags.
package version

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func String() string {
	return fmt.Sprintf("bronto %s (commit %s, built %s)", Version, Commit, Date)
}
```

Run: `go test ./internal/version -v` — Expected: PASS.

- [ ] **Step 4: Root command and main**

`internal/cli/root.go`:

```go
// Package cli contains the Cobra command tree.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/version"
)

func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "bronto",
		Short:         "CLI for the Bronto observability platform",
		Long:          "bronto is a command-line client for the Bronto observability platform.\nDocs: https://docs.bronto.io",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.String(),
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	return cmd
}
```

`cmd/bronto/main.go`:

```go
package main

import (
	"os"

	"github.com/svrnm/bronto-cli/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	cmd := cli.NewRootCmd()
	if err := cmd.Execute(); err != nil {
		// Error rendering is wired properly in Task 2.
		os.Stderr.WriteString(err.Error() + "\n")
		return 1
	}
	return 0
}
```

- [ ] **Step 5: testscript harness + version script**

`cmd/bronto/main_test.go`:

```go
package main

import (
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"bronto": main,
	})
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{Dir: "testdata/script"})
}
```

Note: on go-internal versions before v1.14 the API is `os.Exit(testscript.RunMain(m, map[string]func() int{"bronto": run}))` — use whichever the installed version exposes; do not add another dependency.

`cmd/bronto/testdata/script/version.txtar`:

```txtar
# --version prints version string and exits 0
exec bronto --version
stdout '^bronto dev'

# unknown command exits non-zero
! exec bronto no-such-command
```

Run: `go test ./cmd/bronto -v` — Expected: PASS.
Run: `CGO_ENABLED=0 go build -o /dev/null ./cmd/bronto` — Expected: builds clean.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: scaffold bronto CLI with root command, version, testscript harness"
```

---

### Task 2: Typed errors and exit codes

**Files:**
- Create: `internal/clierr/clierr.go`, `internal/clierr/clierr_test.go`
- Modify: `cmd/bronto/main.go` (error rendering + exit codes)
- Create: `cmd/bronto/testdata/script/exitcodes.txtar`

**Interfaces:**
- Produces:
  - `clierr.New(code, message string) *clierr.Error`
  - `(*Error).WithHint(string) *Error`, `(*Error).WithDocs(string) *Error`, `(*Error).WithRetryable() *Error`
  - `type Error struct { Code, Message, Hint, DocsURL string; Retryable bool }` implementing `error`
  - `(*Error).ExitCode() int`
  - `clierr.Render(w io.Writer, err error, machineMode bool)` — human or JSON envelope
  - `clierr.ExitCode(err error) int` — works on any error (wraps unknown as 1; cobra usage errors as 2)

- [ ] **Step 1: Write failing tests**

`internal/clierr/clierr_test.go`:

```go
package clierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"api_error", 1},
		{"usage_invalid_flag", 2},
		{"config_secret_in_project_file", 2},
		{"auth_invalid_key", 3},
		{"auth_insufficient_role", 3},
		{"dataset_not_found", 4},
		{"rate_limited", 5},
		{"timeout", 5},
	}
	for _, c := range cases {
		if got := New(c.code, "x").ExitCode(); got != c.want {
			t.Errorf("ExitCode(%q) = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestExitCodeUnknownError(t *testing.T) {
	if got := ExitCode(errors.New("boom")); got != 1 {
		t.Fatalf("ExitCode(plain error) = %d, want 1", got)
	}
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("ExitCode(nil) = %d, want 0", got)
	}
}

func TestRenderMachineMode(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, New("rate_limited", "slow down").WithRetryable(), true)
	var env struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	if env.Error.Code != "rate_limited" || !env.Error.Retryable {
		t.Fatalf("bad envelope: %+v", env)
	}
}

func TestRenderHumanIncludesHintAndDocs(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, New("auth_insufficient_role", "403 from API").
		WithHint("You are likely using an ingestion key; create a management key.").
		WithDocs("https://docs.bronto.io/api-reference/api-keys/overview"), false)
	out := buf.String()
	for _, want := range []string{"403 from API", "ingestion key", "docs.bronto.io"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q: %q", want, out)
		}
	}
}
```

Run: `go test ./internal/clierr -v` — Expected: FAIL (undefined symbols).

- [ ] **Step 2: Implement**

`internal/clierr/clierr.go`:

```go
// Package clierr defines the CLI's typed errors: stable machine codes,
// human hints, and the exit-code contract (spec §5).
package clierr

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Error struct {
	Code      string
	Message   string
	Hint      string
	DocsURL   string
	Retryable bool
}

func New(code, message string) *Error { return &Error{Code: code, Message: message} }

func (e *Error) WithHint(h string) *Error    { e.Hint = h; return e }
func (e *Error) WithDocs(u string) *Error    { e.DocsURL = u; return e }
func (e *Error) WithRetryable() *Error       { e.Retryable = true; return e }
func (e *Error) Error() string               { return e.Message }

func (e *Error) ExitCode() int {
	switch {
	case strings.HasPrefix(e.Code, "usage_"), strings.HasPrefix(e.Code, "config_"):
		return 2
	case strings.HasPrefix(e.Code, "auth_"):
		return 3
	case strings.HasSuffix(e.Code, "_not_found"):
		return 4
	case e.Code == "rate_limited", e.Code == "timeout":
		return 5
	default:
		return 1
	}
}

// ExitCode maps any error to the exit-code contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ce *Error
	if ok := asCLIError(err, &ce); ok {
		return ce.ExitCode()
	}
	return 1
}

func asCLIError(err error, target **Error) bool {
	for err != nil {
		if ce, ok := err.(*Error); ok {
			*target = ce
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Render writes err to w. machineMode selects the stable JSON envelope.
func Render(w io.Writer, err error, machineMode bool) {
	var ce *Error
	if !asCLIError(err, &ce) {
		ce = New("error", err.Error())
	}
	if machineMode {
		env := map[string]any{"error": map[string]any{
			"code": ce.Code, "message": ce.Message, "retryable": ce.Retryable,
		}}
		b, _ := json.Marshal(env)
		fmt.Fprintln(w, string(b))
		return
	}
	fmt.Fprintf(w, "Error: %s (%s)\n", ce.Message, ce.Code)
	if ce.Hint != "" {
		fmt.Fprintf(w, "Hint: %s\n", ce.Hint)
	}
	if ce.DocsURL != "" {
		fmt.Fprintf(w, "Docs: %s\n", ce.DocsURL)
	}
}
```

Run: `go test ./internal/clierr -v` — Expected: PASS.

- [ ] **Step 3: Wire into main**

Replace the error branch in `cmd/bronto/main.go` `run()`:

```go
func run() int {
	cmd := cli.NewRootCmd()
	if err := cmd.Execute(); err != nil {
		machine := !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd())
		clierr.Render(os.Stderr, err, machine)
		return clierr.ExitCode(err)
	}
	return 0
}
```

Add `go get github.com/mattn/go-isatty@latest` and the imports (`os`, `github.com/mattn/go-isatty`, `github.com/svrnm/bronto-cli/internal/clierr`).

`cmd/bronto/testdata/script/exitcodes.txtar`:

```txtar
# usage errors (unknown flag) fail with the usage error code on stderr
! exec bronto --definitely-not-a-flag
stderr 'usage_invalid_flag'
```

The exact exit-code number is asserted in a unit test (testscript only distinguishes zero/non-zero portably):

`cmd/bronto/exitcode_test.go`:

```go
package main

import (
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func TestUsageErrorsExitTwo(t *testing.T) {
	err := clierr.New("usage_invalid_flag", "unknown flag")
	if clierr.ExitCode(err) != 2 {
		t.Fatal("usage errors must exit 2")
	}
}
```

Cobra flag-parse errors are plain errors (exit 1 by default); wrap them: in `NewRootCmd()` add

```go
cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
	return clierr.New("usage_invalid_flag", err.Error()).
		WithHint("Run 'bronto --help' for usage.")
})
```

Run: `go test ./... -v` — Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat: typed errors with stable codes and exit-code contract"
```

---

### Task 3: Config resolution (precedence, sources, profiles)

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`
- Create: `internal/config/project.go` (project-file discovery)

**Interfaces:**
- Produces:
  - `type Source string` with constants `SourceFlag "flag"`, `SourceEnv "env"`, `SourceProject "project"`, `SourceUser "user"`, `SourceDefault "default"`
  - `type Value struct { Val string; Source Source }`
  - `type LoadOptions struct { Flags map[string]string; Getenv func(string) string; WorkDir string; UserConfigDir string }`
  - `config.Load(opts LoadOptions) (*Config, error)`
  - `(*Config).Get(key string) (Value, bool)`; `(*Config).Values() map[string]Value`
  - `(*Config).APIKey() string`, `(*Config).BaseURL() string`, `(*Config).Profile() string`
  - Recognized keys: `api_key`, `profile`, `region`, `base_url`, `output`, `default_dataset`, `timeout`
- Consumes: `clierr.New` (Task 2).

**Config file shapes** (documented here, used by tests):

User config `<UserConfigDir>/bronto/config.toml`:

```toml
default_profile = "prod-eu"

[profiles.prod-eu]
region = "eu"
default_dataset = "550e8400-e29b-41d4-a716-446655440000"
output = "table"
```

Project `.bronto.toml` (non-secret only; discovered by walking up from WorkDir):

```toml
profile = "prod-eu"
default_dataset = "550e8400-e29b-41d4-a716-446655440000"
```

- [ ] **Step 1: Write failing tests**

`internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrecedenceFlagBeatsEnvBeatsFiles(t *testing.T) {
	dir := t.TempDir()
	ucd := t.TempDir()
	writeFile(t, filepath.Join(ucd, "bronto", "config.toml"),
		"default_profile = \"p1\"\n[profiles.p1]\nregion = \"us\"\n")
	writeFile(t, filepath.Join(dir, ".bronto.toml"), "region = \"us\"\n")

	cfg, err := Load(LoadOptions{
		Flags:         map[string]string{"region": "eu"},
		Getenv:        env(map[string]string{"BRONTO_REGION": "us"}),
		WorkDir:       dir,
		UserConfigDir: ucd,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := cfg.Get("region")
	if v.Val != "eu" || v.Source != SourceFlag {
		t.Fatalf("got %+v, want eu from flag", v)
	}
}

func TestEnvAPIKeyAndSourceTracking(t *testing.T) {
	cfg, err := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_API_KEY": "sekret"}),
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey() != "sekret" {
		t.Fatal("api key from env not resolved")
	}
	v, _ := cfg.Get("api_key")
	if v.Source != SourceEnv {
		t.Fatalf("source = %s, want env", v.Source)
	}
}

func TestBaseURLDerivedFromRegionDefaultEU(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: t.TempDir(), UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseURL(); got != "https://api.eu.bronto.io" {
		t.Fatalf("BaseURL() = %q", got)
	}
	cfg2, _ := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_REGION": "us"}),
		WorkDir: t.TempDir(), UserConfigDir: t.TempDir(),
	})
	if got := cfg2.BaseURL(); got != "https://api.us.bronto.io" {
		t.Fatalf("BaseURL() = %q", got)
	}
}

func TestProjectFileWalksUpAndRefusesSecrets(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".bronto.toml"), "default_dataset = \"ds-1\"\n")

	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: sub, UserConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := cfg.Get("default_dataset")
	if v.Val != "ds-1" || v.Source != SourceProject {
		t.Fatalf("got %+v, want ds-1 from project", v)
	}

	writeFile(t, filepath.Join(root, ".bronto.toml"), "api_key = \"leaked\"\n")
	_, err = Load(LoadOptions{Getenv: env(nil), WorkDir: sub, UserConfigDir: t.TempDir()})
	if err == nil {
		t.Fatal("want error for secret in project file")
	}
}

func TestProfileSelectionFromUserConfig(t *testing.T) {
	ucd := t.TempDir()
	writeFile(t, filepath.Join(ucd, "bronto", "config.toml"),
		"default_profile = \"stage\"\n[profiles.stage]\nregion = \"us\"\noutput = \"json\"\n[profiles.prod]\nregion = \"eu\"\n")

	cfg, err := Load(LoadOptions{Getenv: env(nil), WorkDir: t.TempDir(), UserConfigDir: ucd})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile() != "stage" {
		t.Fatalf("Profile() = %q", cfg.Profile())
	}
	if cfg.BaseURL() != "https://api.us.bronto.io" {
		t.Fatalf("BaseURL() = %q", cfg.BaseURL())
	}
	// BRONTO_PROFILE env overrides default_profile
	cfg2, _ := Load(LoadOptions{
		Getenv:  env(map[string]string{"BRONTO_PROFILE": "prod"}),
		WorkDir: t.TempDir(), UserConfigDir: ucd,
	})
	if cfg2.BaseURL() != "https://api.eu.bronto.io" {
		t.Fatalf("profile override failed: %q", cfg2.BaseURL())
	}
}
```

Run: `go test ./internal/config -v` — Expected: FAIL (undefined symbols).

- [ ] **Step 2: Implement**

```bash
go get github.com/pelletier/go-toml/v2@latest
```

`internal/config/config.go`:

```go
// Package config resolves CLI configuration with the precedence
// flags > env > project .bronto.toml > user config > defaults (spec §6),
// tracking the source of every value.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

type Source string

const (
	SourceFlag    Source = "flag"
	SourceEnv     Source = "env"
	SourceProject Source = "project"
	SourceUser    Source = "user"
	SourceDefault Source = "default"
)

type Value struct {
	Val    string
	Source Source
}

type LoadOptions struct {
	Flags         map[string]string
	Getenv        func(string) string
	WorkDir       string
	UserConfigDir string // parent dir; config lives at <dir>/bronto/config.toml
}

type Config struct {
	values  map[string]Value
	profile string
}

var envKeys = map[string]string{
	"api_key": "BRONTO_API_KEY",
	"profile": "BRONTO_PROFILE",
	"region":  "BRONTO_REGION",
}

// Keys settable from files (project and user profile sections). api_key is
// deliberately absent: secrets never come from files.
var fileKeys = []string{"profile", "region", "base_url", "output", "default_dataset", "timeout"}

type userFile struct {
	DefaultProfile string                       `toml:"default_profile"`
	Profiles       map[string]map[string]string `toml:"profiles"`
}

func Load(opts LoadOptions) (*Config, error) {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	c := &Config{values: map[string]Value{}}
	set := func(key, val string, src Source) {
		if val == "" {
			return
		}
		if _, exists := c.values[key]; !exists {
			c.values[key] = Value{Val: val, Source: src}
		}
	}

	// 1. flags
	for k, v := range opts.Flags {
		set(k, v, SourceFlag)
	}
	// 2. env
	for key, env := range envKeys {
		set(key, opts.Getenv(env), SourceEnv)
	}
	// 3. project file
	proj, projPath, err := loadProjectFile(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	if proj != nil {
		if _, has := proj["api_key"]; has {
			return nil, clierr.New("config_secret_in_project_file",
				fmt.Sprintf("refusing to read api_key from %s", projPath)).
				WithHint("Move the key to the BRONTO_API_KEY environment variable or run 'bronto auth login'.")
		}
		for _, k := range fileKeys {
			set(k, proj[k], SourceProject)
		}
	}
	// 4. user config (profile section)
	uf, err := loadUserFile(opts.UserConfigDir, opts.Getenv)
	if err != nil {
		return nil, err
	}
	if uf != nil {
		set("profile", uf.DefaultProfile, SourceUser)
		c.profile = c.values["profile"].Val
		if p, ok := uf.Profiles[c.profile]; ok {
			if _, has := p["api_key"]; has {
				return nil, clierr.New("config_secret_in_config_file",
					"refusing to read api_key from the user config file").
					WithHint("Use the BRONTO_API_KEY environment variable or 'bronto auth login' (keychain).")
			}
			for _, k := range fileKeys {
				set(k, p[k], SourceUser)
			}
		}
	} else {
		c.profile = c.values["profile"].Val
	}
	// 5. defaults
	set("region", "eu", SourceDefault)
	return c, nil
}

func loadUserFile(dir string, getenv func(string) string) (*userFile, error) {
	if override := getenv("BRONTO_CONFIG_DIR"); override != "" {
		dir = override
	}
	if dir == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return nil, nil
		}
		dir = d
	}
	path := filepath.Join(dir, "bronto", "config.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // absent is fine
	}
	var uf userFile
	if err := toml.Unmarshal(b, &uf); err != nil {
		return nil, clierr.New("config_parse_error", fmt.Sprintf("cannot parse %s: %v", path, err))
	}
	return &uf, nil
}

func (c *Config) Get(key string) (Value, bool) {
	v, ok := c.values[key]
	return v, ok
}

func (c *Config) Values() map[string]Value {
	out := make(map[string]Value, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

func (c *Config) APIKey() string  { return c.values["api_key"].Val }
func (c *Config) Profile() string { return c.values["profile"].Val }

func (c *Config) BaseURL() string {
	if v, ok := c.values["base_url"]; ok {
		return v.Val
	}
	return fmt.Sprintf("https://api.%s.bronto.io", c.values["region"].Val)
}
```

`internal/config/project.go`:

```go
package config

import (
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// loadProjectFile walks up from dir looking for .bronto.toml (like .git).
// Returns (nil, "", nil) when none exists.
func loadProjectFile(dir string) (map[string]string, string, error) {
	if dir == "" {
		d, err := os.Getwd()
		if err != nil {
			return nil, "", nil
		}
		dir = d
	}
	for {
		path := filepath.Join(dir, ".bronto.toml")
		if b, err := os.ReadFile(path); err == nil {
			var m map[string]string
			if err := toml.Unmarshal(b, &m); err != nil {
				return nil, path, clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
			}
			return m, path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", nil
		}
		dir = parent
	}
}
```

Run: `go test ./internal/config -v` — Expected: PASS.

Note: `TestProjectFileWalksUpAndRefusesSecrets` may find a stray `.bronto.toml` above `t.TempDir()` on a developer machine only if one exists in `/tmp` — acceptable.

- [ ] **Step 3: Commit**

```bash
git add internal/config go.mod go.sum
git commit -m "feat: config resolution with precedence, source tracking, profiles"
```

---

### Task 4: Output engine

**Files:**
- Create: `internal/output/output.go`, `internal/output/output_test.go`
- Create: `internal/output/color.go`, `internal/output/color_test.go`

**Interfaces:**
- Produces:
  - `type Format string`; constants `FormatTable "table"`, `FormatJSON "json"`, `FormatJSONL "jsonl"`, `FormatRaw "raw"`, `FormatCSV "csv"`
  - `output.ParseFormat(s string) (Format, error)` (usage error on unknown)
  - `output.DetectFormat(flagVal string, stdoutIsTTY bool, streaming bool) (Format, error)` — flag wins; else table on TTY; else jsonl if streaming, json otherwise
  - `type Printer struct { ... }`; `output.NewPrinter(w io.Writer, f Format) *Printer`
  - `(*Printer).PrintRows(columns []string, rows []map[string]any) error` — table/json/csv
  - `(*Printer).PrintRow(columns []string, row map[string]any) error` — jsonl/raw streaming (raw prints the `@raw` key if present, else JSON)
  - `(*Printer).PrintJSON(v any) error` — arbitrary document (pretty in table-mode/TTY contexts, compact otherwise)
  - `output.ColorEnabled(noColorFlag bool, isTTY bool, getenv func(string) string) bool`
- Consumes: `clierr.New` (Task 2).

- [ ] **Step 1: Write failing tests**

`internal/output/output_test.go`:

```go
package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var rows = []map[string]any{
	{"name": "web", "count": 3},
	{"name": "db", "count": 1},
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		flag      string
		tty       bool
		streaming bool
		want      Format
	}{
		{"", true, false, FormatTable},
		{"", false, false, FormatJSON},
		{"", false, true, FormatJSONL},
		{"csv", false, false, FormatCSV},
		{"table", false, true, FormatTable},
	}
	for _, c := range cases {
		got, err := DetectFormat(c.flag, c.tty, c.streaming)
		if err != nil || got != c.want {
			t.Errorf("DetectFormat(%q,%v,%v) = %v,%v want %v", c.flag, c.tty, c.streaming, got, err, c.want)
		}
	}
	if _, err := DetectFormat("yamlish", true, false); err == nil {
		t.Error("unknown format must error")
	}
}

func TestJSONOutputIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatJSON).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not a JSON array: %v", err)
	}
	if len(got) != 2 || got[0]["name"] != "web" {
		t.Fatalf("got %+v", got)
	}
}

func TestJSONLOneObjectPerLine(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatJSONL)
	for _, r := range rows {
		if err := p.PrintRow([]string{"name", "count"}, r); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q not JSON: %v", l, err)
		}
	}
}

func TestTableColumnsOrdered(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatTable).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "web") {
		t.Fatalf("table output missing headers/values: %q", out)
	}
	if strings.Index(out, "NAME") > strings.Index(out, "COUNT") {
		t.Fatal("column order not preserved")
	}
}

func TestCSV(t *testing.T) {
	var buf bytes.Buffer
	if err := NewPrinter(&buf, FormatCSV).PrintRows([]string{"name", "count"}, rows); err != nil {
		t.Fatal(err)
	}
	want := "name,count\nweb,3\ndb,1\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestRawPrintsRawField(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, FormatRaw)
	if err := p.PrintRow(nil, map[string]any{"@raw": "hello world", "x": 1}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello world\n" {
		t.Fatalf("got %q", buf.String())
	}
}
```

`internal/output/color_test.go`:

```go
package output

import "testing"

func TestColorEnabled(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		flag bool
		tty  bool
		env  map[string]string
		want bool
	}{
		{"tty default", false, true, nil, true},
		{"pipe default", false, false, nil, false},
		{"flag wins", true, true, map[string]string{"FORCE_COLOR": "1"}, false},
		{"NO_COLOR wins over tty", false, true, map[string]string{"NO_COLOR": "1"}, false},
		{"FORCE_COLOR wins over pipe", false, false, map[string]string{"FORCE_COLOR": "1"}, true},
		{"TERM dumb disables", false, true, map[string]string{"TERM": "dumb"}, false},
	}
	for _, c := range cases {
		if got := ColorEnabled(c.flag, c.tty, env(c.env)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
```

Run: `go test ./internal/output -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/output/output.go`:

```go
// Package output is the single output engine used by every command (spec §5).
// stdout gets data only; formats: table (TTY default), json, jsonl
// (piped streaming default), raw, csv.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatJSONL Format = "jsonl"
	FormatRaw   Format = "raw"
	FormatCSV   Format = "csv"
)

func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatTable, FormatJSON, FormatJSONL, FormatRaw, FormatCSV:
		return Format(s), nil
	}
	return "", clierr.New("usage_invalid_output_format",
		fmt.Sprintf("unknown output format %q", s)).
		WithHint("Valid formats: table, json, jsonl, raw, csv.")
}

func DetectFormat(flagVal string, stdoutIsTTY, streaming bool) (Format, error) {
	if flagVal != "" {
		return ParseFormat(flagVal)
	}
	if stdoutIsTTY {
		return FormatTable, nil
	}
	if streaming {
		return FormatJSONL, nil
	}
	return FormatJSON, nil
}

type Printer struct {
	w      io.Writer
	format Format
}

func NewPrinter(w io.Writer, f Format) *Printer { return &Printer{w: w, format: f} }

func (p *Printer) PrintRows(columns []string, rows []map[string]any) error {
	switch p.format {
	case FormatJSON:
		enc := json.NewEncoder(p.w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case FormatJSONL, FormatRaw:
		for _, r := range rows {
			if err := p.PrintRow(columns, r); err != nil {
				return err
			}
		}
		return nil
	case FormatCSV:
		cw := csv.NewWriter(p.w)
		if err := cw.Write(columns); err != nil {
			return err
		}
		for _, r := range rows {
			rec := make([]string, len(columns))
			for i, c := range columns {
				rec[i] = fmt.Sprint(r[c])
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
		cw.Flush()
		return cw.Error()
	default: // table
		tw := tabwriter.NewWriter(p.w, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, strings.ToUpper(strings.Join(columns, "\t")))
		for _, r := range rows {
			vals := make([]string, len(columns))
			for i, c := range columns {
				vals[i] = fmt.Sprint(r[c])
			}
			fmt.Fprintln(tw, strings.Join(vals, "\t"))
		}
		return tw.Flush()
	}
}

func (p *Printer) PrintRow(columns []string, row map[string]any) error {
	switch p.format {
	case FormatRaw:
		if raw, ok := row["@raw"]; ok {
			_, err := fmt.Fprintln(p.w, raw)
			return err
		}
		fallthrough
	default:
		return json.NewEncoder(p.w).Encode(row)
	}
}

func (p *Printer) PrintJSON(v any) error {
	enc := json.NewEncoder(p.w)
	if p.format == FormatTable { // human context: pretty-print
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}
```

`internal/output/color.go`:

```go
package output

// ColorEnabled implements the precedence
// --no-color flag > NO_COLOR > FORCE_COLOR > TERM=dumb > TTY (spec §5).
func ColorEnabled(noColorFlag, isTTY bool, getenv func(string) string) bool {
	if noColorFlag {
		return false
	}
	if getenv("NO_COLOR") != "" {
		return false
	}
	if getenv("FORCE_COLOR") != "" {
		return true
	}
	if getenv("TERM") == "dumb" {
		return false
	}
	return isTTY
}
```

Run: `go test ./internal/output -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/output
git commit -m "feat: output engine with table/json/jsonl/raw/csv and color rules"
```

---

### Task 5: Vendor OpenAPI spec + generated client

**Files:**
- Create: `api/openapi.yaml` (vendored Bronto spec), `api/oapi-codegen.yaml`
- Create: `internal/api/doc.go`, generated `internal/api/gen.go` (checked in)
- Modify: `go.mod` (tool + runtime deps)

**Interfaces:**
- Produces: package `api` (`internal/api`) containing generated types and `ClientWithResponses`; `//go:generate` wiring; `make generate` / `make check-generate` working.
- Consumes: nothing (generated code is used from Plan 2 onward; Plan 1 commands use the raw transport).

- [ ] **Step 1: Vendor the spec**

```bash
mkdir -p api
cp ~/.claude/skills/bronto/references/openapi.yaml api/openapi.yaml
git add api/openapi.yaml
```

(Canonical upstream: the Bronto API reference at https://docs.bronto.io — the spec-sync CI loop in Plan 6 will fetch from there. The local copy is current as of 2026-07-06.)

- [ ] **Step 2: Generation config + tool dependency**

```bash
go get -tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
go get github.com/oapi-codegen/runtime@latest
```

`api/oapi-codegen.yaml`:

```yaml
package: api
output: internal/api/gen.go
generate:
  models: true
  client: true
```

`internal/api/doc.go`:

```go
// Package api contains the client and types generated from the vendored
// Bronto OpenAPI spec (api/openapi.yaml). Do not edit gen.go by hand;
// run `make generate`.
package api

//go:generate go tool oapi-codegen -config ../../api/oapi-codegen.yaml ../../api/openapi.yaml
```

- [ ] **Step 3: Generate and verify**

```bash
make generate
CGO_ENABLED=0 go build ./...
go vet ./...
```

Expected: `internal/api/gen.go` created; whole module builds.

**Contingency (spec constructs the generator rejects):** if `oapi-codegen` errors, constrain scope with `include-tags` in `api/oapi-codegen.yaml` to the resources the CLI needs first:

```yaml
output-options:
  include-tags: [api-keys, search, logs, monitors, exports, top-keys, usage, context, datasets, dashboards, parsers, tags, saved-searches]
```

Record whatever exclusion was needed in a comment in `api/oapi-codegen.yaml` so Plan 6's spec-sync job knows.

- [ ] **Step 4: Verify regeneration is clean and commit**

```bash
make check-generate   # must exit 0 (no diff after regenerating)
git add -A
git commit -m "feat: vendor Bronto OpenAPI spec and check in generated client"
```

---

### Task 6: HTTP transport (auth, retries, error mapping)

**Files:**
- Create: `internal/api/transport.go`, `internal/api/transport_test.go`

**Interfaces:**
- Produces:
  - `type Transport struct { APIKey, UserAgent string; Base http.RoundTripper; MaxRetries int; Sleep func(time.Duration) }` implementing `http.RoundTripper`
  - `api.NewHTTPClient(apiKey, version string) *http.Client` — Transport with `MaxRetries: 2`, 30 s timeout
  - `api.ErrorFromStatus(status int, body []byte) *clierr.Error` — nil for 2xx; maps 401→`auth_invalid_key`, 403→`auth_insufficient_role` (+ingestion-key hint +docs URL), 404→`resource_not_found`, 429→`rate_limited` (retryable), 5xx→`api_server_error` (retryable), else `api_error`
- Consumes: `clierr` (Task 2).

- [ ] **Step 1: Write failing tests**

`internal/api/transport_test.go`:

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransportSetsHeaders(t *testing.T) {
	var gotKey, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-BRONTO-API-KEY")
		gotUA = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := NewHTTPClient("test-key", "1.2.3")
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotKey != "test-key" {
		t.Fatalf("api key header = %q", gotKey)
	}
	if gotUA != "bronto-cli/1.2.3" {
		t.Fatalf("user agent = %q", gotUA)
	}
}

func TestTransportRetriesOn429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2, Sleep: func(time.Duration) {}}
	c := &http.Client{Transport: tr}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestTransportDoesNotRetryPOST(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr := &Transport{APIKey: "k", UserAgent: "t", MaxRetries: 2, Sleep: func(time.Duration) {}}
	c := &http.Client{Transport: tr}
	resp, err := c.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("POST retried: calls = %d, want 1", calls.Load())
	}
}

func TestErrorFromStatus(t *testing.T) {
	cases := []struct {
		status   int
		code     string
		exit     int
		retryable bool
	}{
		{401, "auth_invalid_key", 3, false},
		{403, "auth_insufficient_role", 3, false},
		{404, "resource_not_found", 4, false},
		{429, "rate_limited", 5, true},
		{502, "api_server_error", 1, true},
		{418, "api_error", 1, false},
	}
	for _, c := range cases {
		e := ErrorFromStatus(c.status, []byte(`{"message":"nope"}`))
		if e == nil {
			t.Fatalf("status %d: nil error", c.status)
		}
		if e.Code != c.code || e.ExitCode() != c.exit || e.Retryable != c.retryable {
			t.Errorf("status %d: got code=%s exit=%d retryable=%v", c.status, e.Code, e.ExitCode(), e.Retryable)
		}
	}
	if ErrorFromStatus(200, nil) != nil {
		t.Error("2xx must map to nil")
	}
}
```

Run: `go test ./internal/api -run 'TestTransport|TestErrorFromStatus' -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/api/transport.go`:

```go
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// Transport adds auth + User-Agent headers and retries idempotent requests
// on 429/502/503/504, honoring Retry-After.
type Transport struct {
	APIKey     string
	UserAgent  string
	Base       http.RoundTripper
	MaxRetries int
	Sleep      func(time.Duration) // injectable for tests; nil = time.Sleep
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	sleep := t.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	req.Header.Set("X-BRONTO-API-KEY", t.APIKey)
	req.Header.Set("User-Agent", t.UserAgent)

	idempotent := req.Method == http.MethodGet || req.Method == http.MethodHead
	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		resp, err = base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if !idempotent || attempt >= t.MaxRetries || !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		resp.Body.Close()
		sleep(retryDelay(resp, attempt))
	}
}

func retryableStatus(s int) bool {
	return s == http.StatusTooManyRequests || s == http.StatusBadGateway ||
		s == http.StatusServiceUnavailable || s == http.StatusGatewayTimeout
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(500*(1<<attempt)) * time.Millisecond
}

func NewHTTPClient(apiKey, version string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &Transport{
			APIKey:     apiKey,
			UserAgent:  "bronto-cli/" + version,
			MaxRetries: 2,
		},
	}
}

// ErrorFromStatus maps a non-2xx API response to a typed error. Nil for 2xx.
func ErrorFromStatus(status int, body []byte) *clierr.Error {
	if status >= 200 && status < 300 {
		return nil
	}
	msg := fmt.Sprintf("Bronto API returned %d", status)
	var apiMsg struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &apiMsg) == nil && apiMsg.Message != "" {
		msg = fmt.Sprintf("Bronto API returned %d: %s", status, apiMsg.Message)
	}
	switch {
	case status == http.StatusUnauthorized:
		return clierr.New("auth_invalid_key", msg).
			WithHint("Check BRONTO_API_KEY or run 'bronto auth status'.")
	case status == http.StatusForbidden:
		return clierr.New("auth_insufficient_role", msg).
			WithHint("You are likely using an ingestion key. This CLI needs a management key (Settings → API Keys in the Bronto UI).").
			WithDocs("https://docs.bronto.io/api-reference/api-keys/overview")
	case status == http.StatusNotFound:
		return clierr.New("resource_not_found", msg)
	case status == http.StatusTooManyRequests:
		return clierr.New("rate_limited", msg).WithRetryable()
	case status >= 500:
		return clierr.New("api_server_error", msg).WithRetryable()
	default:
		return clierr.New("api_error", msg)
	}
}
```

Run: `go test ./internal/api -run 'TestTransport|TestErrorFromStatus' -v` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/api/transport.go internal/api/transport_test.go
git commit -m "feat: HTTP transport with auth, retries, typed error mapping"
```

---

### Task 7: Command context plumbing + `bronto ping`

**Files:**
- Create: `internal/cli/context.go` (shared command context: config, printer, http client)
- Create: `internal/cli/ping.go`, `internal/cli/ping_test.go`
- Modify: `internal/cli/root.go` (persistent flags, command registration)

**Interfaces:**
- Produces:
  - `type App struct { Config *config.Config; Stdout, Stderr io.Writer; HTTPClient *http.Client; StdoutIsTTY bool; OutputFlag string; Quiet bool }`
  - `cli.NewApp(cmd *cobra.Command) (*App, error)` — reads persistent flags, calls `config.Load`, builds `api.NewHTTPClient`
  - `(*App).Printer(streaming bool) (*output.Printer, error)`
  - Persistent flags on root: `--api-key`, `--profile`, `--region`, `--base-url`, `-o/--output`, `--no-color`, `--quiet`
  - `newPingCmd() *cobra.Command` registered on root
- Consumes: `config.Load` (Task 3), `output` (Task 4), `api.NewHTTPClient`/`api.ErrorFromStatus` (Task 6), `clierr` (Task 2), `version` (Task 1).

- [ ] **Step 1: Shared App plumbing**

`internal/cli/context.go`:

```go
package cli

import (
	"io"
	"net/http"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/api"
	"github.com/svrnm/bronto-cli/internal/config"
	"github.com/svrnm/bronto-cli/internal/output"
	"github.com/svrnm/bronto-cli/internal/version"
)

// App bundles everything a command needs. Built once per invocation.
type App struct {
	Config      *config.Config
	Stdout      io.Writer
	Stderr      io.Writer
	HTTPClient  *http.Client
	StdoutIsTTY bool
	OutputFlag  string
	Quiet       bool
}

func NewApp(cmd *cobra.Command) (*App, error) {
	flags := map[string]string{}
	for _, name := range []string{"api-key", "profile", "region", "base-url", "output"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			key := map[string]string{
				"api-key": "api_key", "base-url": "base_url",
				"profile": "profile", "region": "region", "output": "output",
			}[name]
			flags[key] = f.Value.String()
		}
	}
	cfg, err := config.Load(config.LoadOptions{Flags: flags})
	if err != nil {
		return nil, err
	}
	quiet, _ := cmd.Flags().GetBool("quiet")
	outFlag := ""
	if v, ok := cfg.Get("output"); ok {
		outFlag = v.Val
	}
	return &App{
		Config:      cfg,
		Stdout:      cmd.OutOrStdout(),
		Stderr:      cmd.ErrOrStderr(),
		HTTPClient:  api.NewHTTPClient(cfg.APIKey(), version.Version),
		StdoutIsTTY: isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()),
		OutputFlag:  outFlag,
		Quiet:       quiet,
	}, nil
}

func (a *App) Printer(streaming bool) (*output.Printer, error) {
	f, err := output.DetectFormat(a.OutputFlag, a.StdoutIsTTY, streaming)
	if err != nil {
		return nil, err
	}
	return output.NewPrinter(a.Stdout, f), nil
}
```

Add to `NewRootCmd()` in `internal/cli/root.go` (before `return cmd`):

```go
	pf := cmd.PersistentFlags()
	pf.String("api-key", "", "Bronto management API key (prefer BRONTO_API_KEY env)")
	pf.String("profile", "", "named profile to use")
	pf.String("region", "", "Bronto region: eu or us")
	pf.String("base-url", "", "override the API base URL")
	pf.StringP("output", "o", "", "output format: table|json|jsonl|raw|csv")
	pf.Bool("no-color", false, "disable color output")
	pf.Bool("quiet", false, "suppress non-data messages on stderr")
	cmd.AddCommand(newPingCmd())
```

- [ ] **Step 2: Write failing ping test**

`internal/cli/ping_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func execPing(t *testing.T, srvStatus int) (stdout string, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("ping hit %s, want /logs", r.URL.Path)
		}
		w.WriteHeader(srvStatus)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"ping", "--base-url", srv.URL, "--api-key", "k", "-o", "json"})
	err = root.Execute()
	return out.String(), err
}

func TestPingOK(t *testing.T) {
	out, err := execPing(t, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status  string `json:"status"`
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if got.Status != "ok" || got.BaseURL == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestPingForbiddenIsTypedAuthError(t *testing.T) {
	_, err := execPing(t, 403)
	if err == nil {
		t.Fatal("want error")
	}
	if clierr.ExitCode(err) != 3 {
		t.Fatalf("exit code = %d, want 3", clierr.ExitCode(err))
	}
}
```

Run: `go test ./internal/cli -run TestPing -v` — Expected: FAIL (`newPingCmd` undefined).

- [ ] **Step 3: Implement ping**

`internal/cli/ping.go`:

```go
package cli

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/api"
	"github.com/svrnm/bronto-cli/internal/clierr"
)

func newPingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Check connectivity and credentials against the Bronto API",
		Example: "  bronto ping\n  BRONTO_API_KEY=... bronto ping -o json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if app.Config.APIKey() == "" {
				return clierr.New("auth_missing_key", "no API key configured").
					WithHint("Set BRONTO_API_KEY or pass --api-key.")
			}
			start := time.Now()
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet,
				app.Config.BaseURL()+"/logs", nil)
			if err != nil {
				return err
			}
			resp, err := app.HTTPClient.Do(req)
			if err != nil {
				return clierr.New("api_unreachable", fmt.Sprintf("cannot reach %s: %v", app.Config.BaseURL(), err)).
					WithHint("Check your network and the region (--region eu|us).")
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if apiErr := api.ErrorFromStatus(resp.StatusCode, body); apiErr != nil {
				return apiErr
			}
			latency := time.Since(start).Milliseconds()
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			if app.StdoutIsTTY && app.OutputFlag == "" {
				fmt.Fprintf(app.Stdout, "OK — %s (%dms)\n", app.Config.BaseURL(), latency)
				return nil
			}
			return p.PrintJSON(map[string]any{
				"status": "ok", "base_url": app.Config.BaseURL(), "latency_ms": latency,
			})
		},
	}
}
```

Run: `go test ./internal/cli -run TestPing -v` — Expected: PASS.
Run: `go test ./...` — Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cli
git commit -m "feat: app plumbing, persistent flags, and bronto ping"
```

---

### Task 8: `bronto config` (list/get/set)

**Files:**
- Create: `internal/cli/configcmd.go`, `internal/cli/configcmd_test.go`
- Create: `internal/config/write.go`, `internal/config/write_test.go`
- Modify: `internal/cli/root.go` (register command)

**Interfaces:**
- Produces:
  - `config.SetUserValue(userConfigDir, profile, key, value string) error` — writes to `<dir>/bronto/config.toml`, creating file/profile section as needed; rejects `api_key` with `config_secret_rejected`
  - `newConfigCmd() *cobra.Command` with subcommands `list`, `get <key>`, `set <key> <value>`
- Consumes: `config` (Task 3), `output` (Task 4), `App` (Task 7).

- [ ] **Step 1: Write failing tests**

`internal/config/write_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetUserValueRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := SetUserValue(dir, "default", "region", "us"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{
		Getenv:  func(string) string { return "" },
		WorkDir: t.TempDir(), UserConfigDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := cfg.Get("region")
	if !ok || v.Val != "us" || v.Source != SourceUser {
		t.Fatalf("got %+v", v)
	}
	// file landed at the expected path with restrictive permissions
	fi, err := os.Stat(filepath.Join(dir, "bronto", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSetUserValueRejectsSecret(t *testing.T) {
	if err := SetUserValue(t.TempDir(), "default", "api_key", "sekret"); err == nil {
		t.Fatal("want rejection of api_key")
	}
}
```

`internal/cli/configcmd_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestConfigListShowsSources(t *testing.T) {
	t.Setenv("BRONTO_REGION", "us")
	t.Setenv("BRONTO_CONFIG_DIR", t.TempDir())

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"config", "list", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, out.String())
	}
	found := false
	for _, r := range rows {
		if r["key"] == "region" && r["value"] == "us" && r["source"] == "env" {
			found = true
		}
	}
	if !found {
		t.Fatalf("region/us/env row missing: %v", rows)
	}
}
```

Run: `go test ./internal/config ./internal/cli -run 'TestSetUserValue|TestConfigList' -v` — Expected: FAIL.

- [ ] **Step 2: Implement writer**

`internal/config/write.go`:

```go
package config

import (
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

// SetUserValue writes key=value into the profile section of the user config.
// dir is the parent config dir (same semantics as LoadOptions.UserConfigDir).
func SetUserValue(dir, profile, key, value string) error {
	if key == "api_key" {
		return clierr.New("config_secret_rejected", "api_key cannot be stored in the config file").
			WithHint("Use the BRONTO_API_KEY environment variable or 'bronto auth login' (keychain).")
	}
	if profile == "" {
		profile = "default"
	}
	path := filepath.Join(dir, "bronto", "config.toml")
	uf := userFile{Profiles: map[string]map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(b, &uf); err != nil {
			return clierr.New("config_parse_error", "cannot parse "+path+": "+err.Error())
		}
		if uf.Profiles == nil {
			uf.Profiles = map[string]map[string]string{}
		}
	}
	if uf.DefaultProfile == "" {
		uf.DefaultProfile = profile
	}
	if uf.Profiles[profile] == nil {
		uf.Profiles[profile] = map[string]string{}
	}
	uf.Profiles[profile][key] = value

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := toml.Marshal(uf)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
```

- [ ] **Step 3: Implement command**

`internal/cli/configcmd.go`:

```go
package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/clierr"
	"github.com/svrnm/bronto-cli/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and modify bronto configuration",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "Show all resolved config values and where each came from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			vals := app.Config.Values()
			keys := make([]string, 0, len(vals))
			for k := range vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			rows := make([]map[string]any, 0, len(keys))
			for _, k := range keys {
				v := vals[k]
				val := v.Val
				if k == "api_key" && val != "" {
					val = val[:min(8, len(val))] + "…" // never print full secrets
				}
				rows = append(rows, map[string]any{"key": k, "value": val, "source": string(v.Source)})
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintRows([]string{"key", "value", "source"}, rows)
		},
	}

	get := &cobra.Command{
		Use:   "get <key>",
		Short: "Print a single resolved config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			v, ok := app.Config.Get(args[0])
			if !ok {
				return clierr.New("config_key_not_found", fmt.Sprintf("no value for %q", args[0]))
			}
			fmt.Fprintln(app.Stdout, v.Val)
			return nil
		},
	}

	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Persist a config value in the user config file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			dir := os.Getenv("BRONTO_CONFIG_DIR")
			if dir == "" {
				d, err := os.UserConfigDir()
				if err != nil {
					return err
				}
				dir = d
			}
			if err := config.SetUserValue(dir, app.Config.Profile(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(app.Stderr, "Set %s in profile %q\n", args[0], profileOrDefault(app.Config.Profile()))
			return nil
		},
	}

	cmd.AddCommand(list, get, set)
	return cmd
}

func profileOrDefault(p string) string {
	if p == "" {
		return "default"
	}
	return p
}
```

Register in `NewRootCmd()`: `cmd.AddCommand(newConfigCmd())`.

Note: `config.Load` in `NewApp` uses default WorkDir/UserConfigDir (empty strings → cwd and `os.UserConfigDir()`); the `BRONTO_CONFIG_DIR` env override is honored inside `loadUserFile`. The test sets `BRONTO_CONFIG_DIR` accordingly.

Run: `go test ./internal/config ./internal/cli -v` — Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/config internal/cli
git commit -m "feat: bronto config list/get/set with source tracking"
```

---

### Task 9: `bronto api` escape hatch

**Files:**
- Create: `internal/cli/apicmd.go`, `internal/cli/apicmd_test.go`
- Modify: `internal/cli/root.go` (register command)

**Interfaces:**
- Produces: `newAPICmd() *cobra.Command` — `bronto api <METHOD> <path>` with `--field/-f k=v` (repeatable; query params on GET/DELETE, JSON body fields otherwise; values parsed as JSON when they parse, string otherwise) and `--input <file|->` (raw request body; mutually exclusive with body fields).
- Consumes: `App` (Task 7), `api.ErrorFromStatus` (Task 6), `clierr` (Task 2).

- [ ] **Step 1: Write failing tests**

`internal/cli/apicmd_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/svrnm/bronto-cli/internal/clierr"
)

func runAPI(t *testing.T, handler http.HandlerFunc, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	full := append([]string{"api"}, args...)
	full = append(full, "--base-url", srv.URL, "--api-key", "k")
	root.SetArgs(full)
	err := root.Execute()
	return out.String(), err
}

func TestAPIGetWithQueryFields(t *testing.T) {
	out, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/logs" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"logs":[]}`))
	}, "GET", "/logs", "-f", "limit=5")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout not JSON: %v (%q)", err, out)
	}
}

func TestAPIPostBuildsJSONBody(t *testing.T) {
	_, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("body not JSON: %q", b)
		}
		// limit=10 parses as JSON number; name stays string
		if body["limit"] != float64(10) || body["name"] != "x" {
			t.Errorf("body = %v", body)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing content type")
		}
		w.Write([]byte(`{}`))
	}, "POST", "/search", "-f", "limit=10", "-f", "name=x")
	if err != nil {
		t.Fatal(err)
	}
}

func TestAPINon2xxIsTypedError(t *testing.T) {
	_, err := runAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"no such monitor"}`))
	}, "GET", "/monitors/nope")
	if err == nil {
		t.Fatal("want error")
	}
	if clierr.ExitCode(err) != 4 {
		t.Fatalf("exit = %d, want 4", clierr.ExitCode(err))
	}
}

func TestAPIRejectsBadMethod(t *testing.T) {
	root := NewRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"api", "YEET", "/logs", "--api-key", "k"})
	err := root.Execute()
	if err == nil || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage error exit 2, got %v (exit %d)", err, clierr.ExitCode(err))
	}
}
```

Run: `go test ./internal/cli -run TestAPI -v` — Expected: FAIL.

- [ ] **Step 2: Implement**

`internal/cli/apicmd.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svrnm/bronto-cli/internal/api"
	"github.com/svrnm/bronto-cli/internal/clierr"
)

var allowedMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true,
}

func newAPICmd() *cobra.Command {
	var fields []string
	var input string
	cmd := &cobra.Command{
		Use:   "api <METHOD> <path>",
		Short: "Make an authenticated request to any Bronto API endpoint",
		Long: "Escape hatch for endpoints without a dedicated command.\n" +
			"Auth and region resolution are handled for you.",
		Example: "  bronto api GET /logs\n" +
			"  bronto api GET /monitors -f limit=10\n" +
			"  bronto api POST /search --input query.json\n" +
			"  echo '{\"time_range\":\"Last 15 minutes\"}' | bronto api POST /search --input -",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			path := args[1]
			if !allowedMethods[method] {
				return clierr.New("usage_invalid_method", fmt.Sprintf("unsupported HTTP method %q", args[0])).
					WithHint("Use GET, POST, PUT, PATCH, DELETE, or HEAD.")
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			app, err := NewApp(cmd)
			if err != nil {
				return err
			}
			if app.Config.APIKey() == "" {
				return clierr.New("auth_missing_key", "no API key configured").
					WithHint("Set BRONTO_API_KEY or pass --api-key.")
			}

			var body io.Reader
			hasBodyMethod := method == "POST" || method == "PUT" || method == "PATCH"
			switch {
			case input != "" && len(fields) > 0 && hasBodyMethod:
				return clierr.New("usage_conflicting_flags", "--input and --field are mutually exclusive for body requests")
			case input == "-":
				body = cmd.InOrStdin()
			case input != "":
				f, err := os.Open(input)
				if err != nil {
					return clierr.New("usage_input_file", err.Error())
				}
				defer f.Close()
				body = f
			case hasBodyMethod && len(fields) > 0:
				obj := map[string]any{}
				for _, kv := range fields {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return clierr.New("usage_invalid_field", fmt.Sprintf("--field %q is not key=value", kv))
					}
					var parsed any
					if err := json.Unmarshal([]byte(v), &parsed); err == nil {
						obj[k] = parsed
					} else {
						obj[k] = v
					}
				}
				b, err := json.Marshal(obj)
				if err != nil {
					return err
				}
				body = bytes.NewReader(b)
			}

			u := app.Config.BaseURL() + path
			if !hasBodyMethod && len(fields) > 0 {
				q := url.Values{}
				for _, kv := range fields {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return clierr.New("usage_invalid_field", fmt.Sprintf("--field %q is not key=value", kv))
					}
					q.Add(k, v)
				}
				sep := "?"
				if strings.Contains(u, "?") {
					sep = "&"
				}
				u += sep + q.Encode()
			}

			req, err := http.NewRequestWithContext(cmd.Context(), method, u, body)
			if err != nil {
				return err
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := app.HTTPClient.Do(req)
			if err != nil {
				return clierr.New("api_unreachable", err.Error())
			}
			defer resp.Body.Close()
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			if apiErr := api.ErrorFromStatus(resp.StatusCode, respBody); apiErr != nil {
				return apiErr
			}
			if len(respBody) == 0 {
				return nil
			}
			var doc any
			if err := json.Unmarshal(respBody, &doc); err != nil {
				_, err := app.Stdout.Write(respBody) // non-JSON: pass through
				return err
			}
			p, err := app.Printer(false)
			if err != nil {
				return err
			}
			return p.PrintJSON(doc)
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil,
		"key=value pair: query param for GET/DELETE, JSON body field otherwise (repeatable)")
	cmd.Flags().StringVar(&input, "input", "", "request body from file, or - for stdin")
	return cmd
}
```

Register in `NewRootCmd()`: `cmd.AddCommand(newAPICmd())`.

Run: `go test ./internal/cli -run TestAPI -v` — Expected: PASS.
Run: `go test ./...` — Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/cli
git commit -m "feat: bronto api escape hatch with fields, input, typed errors"
```

---

### Task 10: CI workflow

**Files:**
- Create: `.github/workflows/ci.yml`, `.golangci.yml`

**Interfaces:**
- Consumes: `Makefile` targets (Task 1), everything prior.

- [ ] **Step 1: Lint config**

`.golangci.yml`:

```yaml
run:
  timeout: 5m
linters:
  enable:
    - govet
    - staticcheck
    - errcheck
    - unused
    - ineffassign
issues:
  exclude-dirs: []
  exclude-rules:
    - path: internal/api/gen\.go
      linters: [errcheck, staticcheck, unused, ineffassign]
```

- [ ] **Step 2: Workflow**

`.github/workflows/ci.yml`:

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go test ./...
      - run: CGO_ENABLED=0 go build ./...

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: golangci/golangci-lint-action@v6

  generate-clean:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make check-generate

  cross-build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: |
          for os in linux darwin windows; do
            for arch in amd64 arm64; do
              CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -o /dev/null ./cmd/bronto
            done
          done
```

- [ ] **Step 3: Verify locally and commit**

```bash
go test ./...
make build && ./bronto --version && ./bronto ping --help
git add .github .golangci.yml
git commit -m "chore: CI with test matrix, lint, generation-clean and cross-build checks"
```

Expected: all local commands succeed; `./bronto --version` prints `bronto dev (commit none, built unknown)`.

---

## Verification (end of plan)

```bash
go test ./...                                # all green
make check-generate                          # no drift
CGO_ENABLED=0 make build                     # static binary
BRONTO_API_KEY=<real-key> ./bronto ping      # against the real API (manual)
BRONTO_API_KEY=<real-key> ./bronto api GET /logs | head    # machine output, JSON
./bronto config list                         # sources shown
```

Manual acceptance (needs a real management key): `ping` returns OK with latency; `api GET /logs` prints JSON; a wrong key exits `3` with the ingestion-key hint on 403.
