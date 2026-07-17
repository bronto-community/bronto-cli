package cli

import (
	"testing"

	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/config"
)

func TestResolveDatasetExplicitFlagsWin(t *testing.T) {
	app := &App{Config: &config.Config{}}
	ids, expr, err := resolveDataset(app, []string{"11111111-1111-1111-1111-111111111111"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "11111111-1111-1111-1111-111111111111" || expr != "" {
		t.Fatalf("ids=%v expr=%q", ids, expr)
	}
}

func TestResolveDatasetDefaultDatasetUUID(t *testing.T) {
	cfg, err := config.Load(config.LoadOptions{
		Flags:         map[string]string{"default_dataset": "22222222-2222-2222-2222-222222222222"},
		Getenv:        func(string) string { return "" },
		WorkDir:       t.TempDir(),
		UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg}
	ids, expr, err := resolveDataset(app, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "22222222-2222-2222-2222-222222222222" || expr != "" {
		t.Fatalf("ids=%v expr=%q, want the default_dataset UUID alone", ids, expr)
	}
}

func TestResolveDatasetDefaultDatasetExpression(t *testing.T) {
	cfg, err := config.Load(config.LoadOptions{
		Flags:         map[string]string{"default_dataset": "logset = 'prod'"},
		Getenv:        func(string) string { return "" },
		WorkDir:       t.TempDir(),
		UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg}
	ids, expr, err := resolveDataset(app, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if ids != nil || expr != "logset = 'prod'" {
		t.Fatalf("ids=%v expr=%q, want expression form with nil ids", ids, expr)
	}
}

func TestResolveDatasetNoneSelectedIsUsageError(t *testing.T) {
	app := &App{Config: &config.Config{}}
	_, _, err := resolveDataset(app, nil, "")
	if clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage exit 2, got %v", err)
	}
}
