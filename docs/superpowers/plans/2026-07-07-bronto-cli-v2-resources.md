# bronto-cli v2 — Plan 5: Resource Commands, --jq/--fields, Plugins, Completions

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Broad resource coverage (`monitors`, `dashboards`, `datasets`, `parsers`, `api-keys`, `tags`, `saved-searches` — uniform `list|get|create|update|delete`), the `exports` workflow (`create --wait` → poll → download) and `usage`, machine-output power tools (`--jq`, `--fields`), kubectl/gh-style exec plugins, shell completions — plus Plan 4's deferred cleanups.

**Architecture note (documented deviation from spec §4):** the spec envisioned Stripe-style CODE-GENERATED resource commands. This plan implements the same UX with a **descriptor registry + generic command factory** (one table entry per resource driving uniform subcommands) plus a **spec-conformance test** that fails CI if any descriptor path is absent from `api/openapi.yaml`. Same thin-by-contract commands, same spec coupling, far less machinery; literal codegen can replace the table later with zero UX change. The final summary to the user must mention this deviation.

**Tech Stack:** One NEW dependency: `github.com/itchyny/gojq` (pure Go, spec §5 names it). Everything else existing.

**API facts (binding, from the Bronto catalog):**
- monitors: `GET|POST /monitors`, `GET|PATCH|DELETE /monitors/{id}`; extras `GET /monitors/{id}/events`, `POST /monitors/{id}/mute`, `POST /monitors/send-test-notifications`.
- dashboards `GET|POST /dashboards`, `GET|PATCH|DELETE /dashboards/{id}`; saved-searches likewise; parsers `GET|POST /parsers`, `GET|PATCH|DELETE /parsers/{parser_id}`; api-keys `GET|POST /api-keys`, `GET|PATCH|DELETE /api-keys/{id}`; tags `GET|POST|PATCH|DELETE /tags` (+ `/tags/search`).
- datasets: streams live under `GET /logs`, `GET|PUT|DELETE /logs/{logId}`; creation is `POST /datasets` with body `{"collection","dataset"}`.
- exports: `POST /exports` → `GET /exports/{id}` poll until `status: "COMPLETE"` → download `location` URL (plain GET, presigned — no auth header needed, follow redirects). `GET /exports` lists.
- usage: `GET /usage` (+ `/usage/organizations/logs`, `/usage/users`).

## Global Constraints

- Module `github.com/svrnm/bronto-cli`; Go `1.25.0`; `CGO_ENABLED=0 go build ./...`; gofmt clean; golangci-lint 0 issues per commit (`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`).
- Dep allowlist adds `github.com/itchyny/gojq` (runtime).
- stdout data only; confirmations to stderr; NEVER prompt when stdin is not a TTY — destructive ops then REQUIRE `--yes` (else `usage_confirmation_required`, exit 2).
- Exit codes/error codes as established. Machine output stable.
- Conventional commits. Every subcommand: Short + Example help.

**Existing interfaces:** `App/NewApp` (+ `stdoutIsTTY`/`stdinIsTTY` seams, `maskSecret`, `validatePositive`, `configDir`), `bronto.NewClient/GetJSON`, `bronto.EventColumns/Flatten`, `output` engine, `clierr`, `timerange`, `apicmd.go`'s field/body building (extract shared helpers as specified below), `cli.Execute`.

---

### Task 1: Plan-4 deferred cleanups

**Files:** `internal/cli/auth.go`, `internal/cli/send.go`, `internal/secrets/secrets.go`, `internal/ingest/ingest.go` (+ tests).

Fix, each with a test where behavioral:
1. **detectRegion probes**: per-probe `context.WithTimeout(ctx, 5*time.Second)`; when NO candidate produced an HTTP response (all network errors), return `network_error` (retryable) instead of `auth_invalid_key`; keep `auth_invalid_key` when at least one candidate answered 401/403. Test with an unreachable base URL → exit 1 network_error.
2. **`--region` validation** in auth login: only `eu|us` (or empty) → else `usage_invalid_region`. Test.
3. **secrets.Delete semantics**: return nil only when BOTH stores report gone/not-found OR one deleted successfully AND the other is not-found/unsupported; a genuine file I/O error must surface even if keyring deletion succeeded (and vice versa); second logout on a headless box (keyring error + file not-found) → nil (idempotent). Rework the truth table with unit tests using `keyring.MockInitWithError` combinations.
4. **credentials file hardening**: `fileStore` fails with `config_parse_error`-style typed error when an EXISTING credentials file cannot be parsed (do not silently rewrite, which would drop other profiles); after any successful write, `_ = os.Chmod(path, 0o600)` to repair loose permissions. Tests: corrupt file → Store errors; pre-chmod 0644 file → 0600 after Store.
5. **ingest**: `enc.SetEscapeHTML(false)` (test: message with `<>&` round-trips unescaped); LineToEvent tolerates leading whitespace before `{` (TrimLeft check). 
6. **send.go**: remove the dead `region := "eu"` fallback (config default covers it); one-shot detection via `cmd.Flags().Changed("message")` so `-m ""` errors (`usage_missing_message`) instead of silently reading stdin. Tests.

Commit: `refactor: plan-4 deferred cleanups (auth probes, secrets semantics, ingest polish)`. Standard verification gauntlet before commit.

---

### Task 2: `--jq` and `--fields` output tools

**Files:** `internal/output/output.go`(+jq.go), tests; `internal/cli/context.go` (wire flags), `internal/cli/root.go` (persistent flags); `go.mod`.

**Interfaces:**
- Root persistent flags: `--jq <expr>` and `--fields <a,b,c>`; `--fields` with the literal value `?` lists the available field names of the result (one per line on stdout) instead of the data.
- `output.CompileJQ(expr string) (*gojq.Code, error)` — parse+compile once; error → `usage_invalid_jq` (exit 2). Compile in NewApp so bad expressions fail before any network call.
- Printer additions: `SetFieldFilter(fields []string)` — for table/csv: those become the columns (overriding the caller's columns); for json/jsonl: each row filtered to those keys. `SetJQ(code *gojq.Code)` — applied per emitted document (each row for jsonl/PrintRows-json-array-elements; the whole doc for PrintJSON): run gojq, emit EVERY result value as its own JSON line (jq semantics); non-JSON-object outputs (strings, numbers) print as raw JSON values. jq applies ONLY to machine formats (json/jsonl); combined with table/csv/raw → `usage_invalid_flags` ("--jq requires -o json or jsonl").
- `--fields ?` handling lives in the command layer helper: `printAvailableFields(app, rows)` — union of keys, sorted, one per line.
- Wire into `App`: `App.FieldFilter []string`, `App.JQ *gojq.Code`; `App.Printer(...)` applies both to the returned Printer. All existing commands get the behavior for free since they print through the engine. jq+fields precedence: fields filter first, then jq.

Tests (binding):
```go
// output package: JQ on jsonl rows
p := NewPrinter(&buf, FormatJSONL); code, _ := CompileJQ(".name")
p.SetJQ(code)
p.PrintRow(nil, map[string]any{"name":"web","x":1})  // -> "\"web\"\n"
// fields filter on json array
p2 := NewPrinter(&buf2, FormatJSON); p2.SetFieldFilter([]string{"name"})
p2.PrintRows([]string{"name","count"}, rows)  // -> [{"name":"web"},{"name":"db"}]
// CLI: bronto search ... --jq '.["@raw"]' piped -> raw strings as JSON
// CLI: --jq with -o table -> usage exit 2; bad expr -> usage exit 2 BEFORE network (no httptest server needed)
// CLI: --fields ? with search -> lists field names, no data rows
```
`go get github.com/itchyny/gojq@latest`. Commit: `feat: --jq expressions and --fields selection on all machine output`.

---

### Task 3: Resource descriptor registry + generic CRUD commands

**Files:** Create `internal/cli/resources.go`, `internal/cli/resources_test.go`, `internal/cli/resourcespec_test.go`; modify `internal/cli/apicmd.go` (extract shared helpers), `internal/cli/root.go`.

**Interfaces:**
- Extract from apicmd.go into shared package-level helpers (apicmd keeps using them): `parseFieldArgs(fields []string) (map[string]any, error)` (k=v with JSON value inference) and `readBodyInput(cmd *cobra.Command, input string) ([]byte, error)` (file or `-` stdin).
- `type resourceDesc struct { Name, Singular, Base string; IDBase string /* defaults Base */; CreatePath string /* defaults Base */; UpdateMethod string /* "PATCH" or "PUT" */; ListRowKeys []string /* payload keys to try for the rows array; nil = auto */; Columns []string /* nil = auto via bronto.EventColumns cap 8 */; NoCreate, NoUpdate, NoDelete, NoGet bool }`
- `var resourceRegistry = []resourceDesc{...}` — monitors, dashboards, `saved-searches`, parsers, `api-keys`, tags (all Base="/"+name, PATCH), datasets (Base "/logs", CreatePath "/datasets", UpdateMethod "PUT").
- `newResourceCmd(desc) *cobra.Command` producing:
  - `list` — GET Base; rows via `rowsFromPayload(payload, desc.ListRowKeys...)`: try each key for a `[]any`; else if payload is a JSON array use it; else if the object has exactly ONE array-valued key use that; else wrap the object as a single row. Print rows (columns per desc or auto), `Printer(false)`.
  - `get <id>` — GET IDBase+"/"+id → PrintJSON.
  - `create` — `--input file|-` XOR `-f k=v...` (at least one required) → POST CreatePath → PrintJSON response.
  - `update <id>` — same body sources → desc.UpdateMethod IDBase/id → PrintJSON.
  - `delete <id>` — TTY(stdin+stdout): stderr prompt `Delete <singular> <id>? [y/N]:` read line, anything but y/Y aborts (exit 0, "Aborted." stderr); non-TTY or `--yes`: proceed. Non-TTY without `--yes` → `usage_confirmation_required`. 204/200 → stderr confirmation.
  - All verbs route through a thin `resourceClient` using `app.HTTPClient` + `api.ErrorFromStatus` (reuse the request-building shape of apicmd's RunE — factor a `doJSONRequest(ctx, app, method, path string, body []byte) (payload any, err error)` helper used by both).
- Monitors extras (hand-written, attached to the monitors command): `events <id>` (GET, rows), `mute <id>` (POST, stderr confirm), `test` (POST /monitors/send-test-notifications, stderr confirm).
- Registration: `for _, d := range resourceRegistry { cmd.AddCommand(newResourceCmd(d)) }` in root.
- **Spec-conformance test** (`resourcespec_test.go`): read `api/openapi.yaml` once; for every descriptor assert `Base`, `CreatePath`, and `IDBase+"/{"` path prefixes appear as spec paths (line-match `"  <path>:"` or prefix for the {id} forms — the spec's parameter names vary: match `IDBase+"/{"`). This is the CI tripwire replacing codegen drift checks.

Tests (behavioral, httptest end-to-end): monitors list (rows from `{"monitors":[...]}` AND bare-array responses), get, create via -f (assert method/path/body), update PATCH, delete with --yes (assert DELETE path) and non-TTY-without---yes → exit 2; datasets create posts to /datasets while list hits /logs; api-keys list auto-columns. Plus the spec-conformance test.

Commit: `feat: uniform resource commands from a spec-checked descriptor registry`.

---

### Task 4: `exports` workflow + `usage`

**Files:** Create `internal/cli/exports.go`, `internal/cli/usage.go` (+ tests); modify root.go.

**Interfaces:**
- `bronto exports create [--input body.json|-] [-f k=v...] [--where q --dataset id --since 1h]` — EITHER raw body OR the convenience flags (build `{"search_details":{from:[dataset], time_range/…, where}}` via timerange); `--wait` polls `GET /exports/{id}` every 3s (ctx-aware sleep) until `status` ∈ {COMPLETE → proceed, FAILED → `export_failed` exit 1}; `--download <path>` (implies --wait) GETs the `location` URL with a PLAIN http client (presigned; no auth header — use `http.DefaultClient`-style fresh client, follow redirects) streaming to the file; progress notes → stderr.
- `bronto exports list` / `get <id>` / `delete <id>` — via the Task 3 factory? exports differ enough (no update) — add descriptor `{Name:"exports", NoUpdate:true}` to the registry AND attach the hand-written `create` (replacing the factory's) — the factory must support overriding a verb: give `newResourceCmd` a variadic `extras ...*cobra.Command` where an extra with the same Use-verb replaces the generated one.
- `bronto usage [--since 7d] [--dataset id]` — GET /usage with `time_range` (+ `log_id` param when dataset given; single-unit rule like fields); render rows or PrintJSON depending on payload shape (reuse rowsFromPayload).

Tests: create with convenience flags asserts body shape; --wait polls (httptest returns PENDING then COMPLETE) and --download writes the file (second httptest server as the presigned location — assert NO auth header on that request); FAILED → exit 1; usage builds params.

Commit: `feat: exports workflow (create/wait/download) and usage command`.

---

### Task 5: Exec plugins + completions

**Files:** Create `internal/cli/plugins.go`, `internal/cli/plugins_test.go`; modify `cmd/bronto/main.go` or root wiring; `cmd/bronto/testdata/script/completion.txtar`.

**Interfaces:**
- Plugin dispatch (kubectl/gh pattern): when cobra reports an UNKNOWN COMMAND for `bronto foo ...`, before surfacing the error, look for `bronto-foo` on PATH (`exec.LookPath`); found → `syscall.Exec`-style run via `os/exec` with args passed through, stdin/out/err inherited, `BRONTO_*` env passed implicitly (it's the environment); exit with the plugin's exit code (map `*exec.ExitError`). Not found → original unknown-command error. Implement in `cli.Execute` (the shared entry point): intercept the typed unknown-command case; expose `var lookPath = exec.LookPath` + `var runPlugin = func(path string, args []string, ...) (int, error)` seams for tests. IMPORTANT: plugin names must be validated (`^[a-z0-9][a-z0-9-]*$`) before PATH lookup.
- `bronto plugins list` — scan PATH dirs for `bronto-*` executables, print name+path rows.
- Completions: cobra auto-registers `completion`; verify it's enabled and add `cmd/bronto/testdata/script/completion.txtar`: `exec bronto completion bash` succeeds and stdout contains `bronto`; ensure the root command sets `ValidArgsFunction`-friendly behavior is NOT required (out of scope) — just ship the standard six-shell command.
- Plugin exit-code contract: plugin's own exit code passes through verbatim (document: plugins own their exit codes).

Tests: with `lookPath`/`runPlugin` stubbed — `bronto foo` dispatches to `bronto-foo` with remaining args and returns its exit code; unknown command without a plugin still exits 2 with usage_invalid_args; `plugins list` finds a fake executable in a temp PATH dir (real filesystem, real LookPath — build a tiny `bronto-hello` shell script chmod +x); invalid plugin-ish names (`bronto ../evil`) never hit lookPath. The txtar completion test.

Commit: `feat: kubectl-style exec plugins and shell completion coverage`.

---

### Task 6: End-of-plan verification

- `go test ./...`; `make build`; live smoke: `./bronto monitors --help`, `./bronto monitors list --api-key k --base-url <dead>` → typed network_error; `./bronto datasets delete x --api-key k </dev/null` → usage_confirmation_required exit 2; `./bronto search x --jq '.x' -o table` → usage exit 2; `PATH=...: ./bronto hello` plugin dispatch with a real script.
- Whole-branch review (controller dispatches), fix wave, merge.
