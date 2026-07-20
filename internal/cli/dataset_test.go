package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/config"
)

// datasetTestApp builds an App wired to a stub /logs listing. The returned
// counter reports how many /logs requests were made (UUID inputs must not
// trigger a lookup).
func datasetTestApp(t *testing.T, logsJSON string) (*App, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logs" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		calls++
		_, _ = w.Write([]byte(logsJSON))
	}))
	t.Cleanup(srv.Close)
	cfg, err := config.Load(config.LoadOptions{
		Flags:         map[string]string{"base_url": srv.URL, "api_key": "k"},
		Getenv:        func(string) string { return "" },
		WorkDir:       t.TempDir(),
		UserConfigDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &App{Config: cfg, HTTPClient: api.NewHTTPClient("k", "test"), Stderr: &strings.Builder{}}, &calls
}

const twoDatasets = `{"logs":[
	{"log":"web","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"},
	{"log":"app","collection":"prod","log_id":"22222222-2222-2222-2222-222222222222"}]}`

// dupDatasets has "logs" duplicated across two collections — the shape
// that makes bare-name resolution ambiguous.
const dupDatasets = `{"logs":[
	{"log":"logs","collection":"prod","log_id":"33333333-3333-3333-3333-333333333333"},
	{"log":"logs","collection":"staging","log_id":"44444444-4444-4444-4444-444444444444"},
	{"log":"web","collection":"prod","log_id":"11111111-1111-1111-1111-111111111111"}]}`

func TestResolveDatasetUUIDPassesThroughWithoutLookup(t *testing.T) {
	app, calls := datasetTestApp(t, twoDatasets)
	ids, expr, err := resolveDataset(context.Background(), app, []string{"11111111-1111-1111-1111-111111111111"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "11111111-1111-1111-1111-111111111111" || expr != "" {
		t.Fatalf("ids=%v expr=%q", ids, expr)
	}
	if *calls != 0 {
		t.Fatalf("UUID input must not trigger a /logs lookup (got %d calls)", *calls)
	}
}

func TestResolveDatasetByName(t *testing.T) {
	app, _ := datasetTestApp(t, twoDatasets)
	ids, _, err := resolveDataset(context.Background(), app, []string{"app"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("ids=%v, want app's log_id", ids)
	}
}

func TestResolveDatasetUnknownNameListsAvailable(t *testing.T) {
	app, _ := datasetTestApp(t, twoDatasets)
	_, _, err := resolveDataset(context.Background(), app, []string{"nope"}, "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "dataset_not_found" {
		t.Fatalf("want dataset_not_found, got %v", err)
	}
	if !strings.Contains(ce.Hint, "app") || !strings.Contains(ce.Hint, "web") {
		t.Fatalf("hint must name the available datasets: %q", ce.Hint)
	}
}

func TestResolveDatasetNoneSelectedSingleAutoPicks(t *testing.T) {
	app, _ := datasetTestApp(t, `{"logs":[{"log":"only","log_id":"33333333-3333-3333-3333-333333333333"}]}`)
	ids, expr, err := resolveDataset(context.Background(), app, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "33333333-3333-3333-3333-333333333333" || expr != "" {
		t.Fatalf("ids=%v expr=%q, want the sole dataset auto-picked", ids, expr)
	}
}

func TestResolveDatasetNoneSelectedMultipleListsNames(t *testing.T) {
	app, _ := datasetTestApp(t, twoDatasets)
	_, _, err := resolveDataset(context.Background(), app, nil, "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_missing_dataset" || clierr.ExitCode(err) != 2 {
		t.Fatalf("want usage_missing_dataset exit 2, got %v", err)
	}
	if !strings.Contains(ce.Hint, "app") || !strings.Contains(ce.Hint, "web") || !strings.Contains(ce.Hint, "-d <name>") {
		t.Fatalf("hint must list dataset names and how to pick one: %q", ce.Hint)
	}
}

func TestResolveDatasetNoneSelectedEmptyAccount(t *testing.T) {
	app, _ := datasetTestApp(t, `{"logs":[]}`)
	_, _, err := resolveDataset(context.Background(), app, nil, "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_missing_dataset" {
		t.Fatalf("want usage_missing_dataset, got %v", err)
	}
	if !strings.Contains(ce.Hint, "bronto send") {
		t.Fatalf("empty-account hint must point at ingestion: %q", ce.Hint)
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
	ids, expr, err := resolveDataset(context.Background(), app, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "22222222-2222-2222-2222-222222222222" || expr != "" {
		t.Fatalf("ids=%v expr=%q, want the default_dataset UUID alone", ids, expr)
	}
}

func TestResolveDatasetDefaultDatasetName(t *testing.T) {
	app, _ := datasetTestApp(t, twoDatasets)
	app.Config.Inject("default_dataset", "web", config.SourceUser)
	ids, _, err := resolveDataset(context.Background(), app, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("ids=%v, want web's log_id via default_dataset name", ids)
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
	ids, expr, err := resolveDataset(context.Background(), app, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if ids != nil || expr != "logset = 'prod'" {
		t.Fatalf("ids=%v expr=%q, want expression form with nil ids", ids, expr)
	}
}

func TestResolveDatasetQualifiedName(t *testing.T) {
	app, _ := datasetTestApp(t, dupDatasets)
	ids, _, err := resolveDataset(context.Background(), app, []string{"staging/logs"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("ids = %v, want staging/logs' id", ids)
	}
}

func TestResolveDatasetAmbiguousTeachesQualification(t *testing.T) {
	app, _ := datasetTestApp(t, dupDatasets)
	_, _, err := resolveDataset(context.Background(), app, []string{"logs"}, "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_ambiguous_dataset" {
		t.Fatalf("want usage_ambiguous_dataset, got %v", err)
	}
	if !strings.Contains(ce.Message, "prod/logs") || !strings.Contains(ce.Message, "staging/logs") {
		t.Fatalf("message must list qualified candidates: %q", ce.Message)
	}
	if !strings.Contains(ce.Hint, "-d prod/logs") {
		t.Fatalf("hint must show a copy-pasteable qualified example: %q", ce.Hint)
	}
}

func TestResolveDatasetUnknownQualifiedListsQualified(t *testing.T) {
	app, _ := datasetTestApp(t, dupDatasets)
	_, _, err := resolveDataset(context.Background(), app, []string{"nope/nothing"}, "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "dataset_not_found" {
		t.Fatalf("want dataset_not_found, got %v", err)
	}
	if !strings.Contains(ce.Hint, "staging/logs") {
		t.Fatalf("hint must list qualified names: %q", ce.Hint)
	}
}

func TestResolveDatasetPickListQualifiesOnlyDuplicates(t *testing.T) {
	app, _ := datasetTestApp(t, dupDatasets)
	_, _, err := resolveDataset(context.Background(), app, nil, "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "usage_missing_dataset" {
		t.Fatalf("want usage_missing_dataset, got %v", err)
	}
	// duplicated name shown qualified, unique name bare
	if !strings.Contains(ce.Hint, "prod/logs") || !strings.Contains(ce.Hint, "staging/logs") {
		t.Fatalf("duplicates must be qualified in the pick-list: %q", ce.Hint)
	}
	if strings.Contains(ce.Hint, "prod/web") {
		t.Fatalf("unique names must stay bare: %q", ce.Hint)
	}
}

func TestResolveDatasetDefaultDatasetQualified(t *testing.T) {
	app, _ := datasetTestApp(t, dupDatasets)
	app.Config.Inject("default_dataset", "prod/logs", config.SourceUser)
	ids, _, err := resolveDataset(context.Background(), app, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("ids = %v, want prod/logs via default_dataset", ids)
	}
}
