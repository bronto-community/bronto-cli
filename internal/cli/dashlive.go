package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/clierr"
)

// Ownership tags. Live (2026-10-21, EU) dashboards and widgets keep tags
// only through POST /tags: the create and update bodies drop a tags
// field. Tag names can't hold "/" (the API turns it into "_"), so the
// owner tag is bronto-cli-owner, not bronto-cli/owner. Metric
// definitions can't be tagged at all (the tags API's entity types are
// widget, log, api_key, user, dashboard, saved_search), so a definition
// belongs to the dashboard through its widget: it's owned when an owned
// widget references it and no other widget does.
const (
	tagManagedBy      = "managed-by"
	tagManagedByValue = "bronto-cli"
	tagOwner          = "bronto-cli-owner"
)

func isManagedTag(k string) bool { return k == tagManagedBy || k == tagOwner }

// definitionMarker is the description apply gives every definition it
// creates. Ownership doesn't depend on it; it only lets a later apply
// find and delete a definition an interrupted run created but never
// attached to a widget (see dashPlan.Orphans).
func definitionMarker(dashID string) string {
	return "Managed by bronto-cli for dashboard " + dashID + "."
}

type liveLayout struct {
	ID string  `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	W  float64 `json:"w"`
	H  float64 `json:"h"`
}

type liveDashboard struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
	WidgetIDs   []string          `json:"widget_ids"`
	Layout      struct {
		WidgetLayouts []liveLayout `json:"widget_layouts"`
	} `json:"layout"`
}

type liveWidget struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Aux         map[string]any    `json:"aux"`
	MetricIDs   []string          `json:"metric_ids"`
	WidgetIDs   []string          `json:"widget_ids"`
	Tags        map[string]string `json:"tags"`
}

// liveState is what apply and export read: the dashboard (nil when none
// has the name), every widget and every definition in the account. A
// re-apply of an unchanged manifest is these four reads (plus GET /logs
// for dataset names) and nothing else.
type liveState struct {
	Dashboard *liveDashboard
	Widgets   []liveWidget
	Defs      map[string]apiDef
}

// ownedBy reports whether apply created (or adopted) w for dashboard id.
func (w liveWidget) ownedBy(dashID string) bool {
	return dashID != "" && w.Tags[tagManagedBy] == tagManagedByValue && w.Tags[tagOwner] == dashID
}

// ownedElsewhere reports whether w belongs to another dashboard's
// manifest: apply never adopts it, and export writes it as a ref.
func (w liveWidget) ownedElsewhere(dashID string) bool {
	return w.Tags[tagManagedBy] == tagManagedByValue && w.Tags[tagOwner] != "" && w.Tags[tagOwner] != dashID
}

// recode converts a decoded JSON value into a typed struct.
func recode(v any, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// findDashboardByName returns the one dashboard with this exact name, nil
// when there is none, and an error listing the candidates when several
// share it: a manifest's name is its identity, so apply can't guess.
func findDashboardByName(ctx context.Context, app *App, name string) (*liveDashboard, error) {
	payload, err := doJSONRequest(ctx, app, http.MethodGet, "/dashboards", nil)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, row := range rowsFromPayload(payload, "dashboards") {
		if n, _ := row["name"].(string); n == name {
			if id, _ := row["id"].(string); id != "" {
				ids = append(ids, id)
			}
		}
	}
	switch len(ids) {
	case 0:
		return nil, nil
	case 1:
		return getDashboard(ctx, app, ids[0])
	}
	desc, _ := descByName("dashboards")
	return nil, clierr.New(ambiguousCode(desc),
		fmt.Sprintf("%d dashboards are named %q: %s", len(ids), name, strings.Join(ids, ", "))).
		WithHint("apply identifies a dashboard by its name. Rename or delete the extra ones " +
			"('bronto dashboards update <id> -f name=<new name>', 'bronto dashboards delete <id>'), then apply again.")
}

func getDashboard(ctx context.Context, app *App, id string) (*liveDashboard, error) {
	payload, err := doJSONRequest(ctx, app, http.MethodGet, "/dashboards/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var d liveDashboard
	if err := recode(payload, &d); err != nil {
		return nil, err
	}
	if d.ID == "" {
		d.ID = id
	}
	return &d, nil
}

// fetchWidgetsAndDefs reads every widget and every metric definition, one
// list call each.
func fetchWidgetsAndDefs(ctx context.Context, app *App, st *liveState) error {
	payload, err := doJSONRequest(ctx, app, http.MethodGet, "/widgets", nil)
	if err != nil {
		return err
	}
	for _, row := range rowsFromPayload(payload, "widgets") {
		var w liveWidget
		if err := recode(row, &w); err != nil {
			return err
		}
		if w.ID != "" {
			st.Widgets = append(st.Widgets, w)
		}
	}
	payload, err = doJSONRequest(ctx, app, http.MethodGet, "/metrics/definitions", nil)
	if err != nil {
		return err
	}
	st.Defs = map[string]apiDef{}
	for _, row := range rowsFromPayload(payload, "definitions") {
		var d apiDef
		if err := recode(row, &d); err != nil {
			return err
		}
		if d.ID != "" {
			st.Defs[d.ID] = d
		}
	}
	return nil
}
