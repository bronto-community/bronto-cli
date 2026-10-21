package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// executePlan makes the plan's writes. Every step is ordered so that a
// run that stops halfway (a network error, Ctrl-C) leaves state the next
// apply finishes, never a dangling reference:
//
//   - A new widget is attached before it is tagged as owned: if the run
//     stops in between, the next apply finds an attached widget with the
//     manifest's name and adopts it instead of creating a second one.
//   - A changed query gets a new definition; the widget is repointed
//     (full PUT) and only then is the old definition deleted. Deleting
//     first would leave the widget plotting a definition that no longer
//     exists if the run stopped before the repoint.
//   - A removed widget's definitions are deleted before the widget: a
//     widget whose definition is gone is still tagged and is deleted next
//     time, while a definition whose widget is gone can't be found again.
//   - Every definition apply creates carries definitionMarker, so one
//     created by a run that stopped before its widget pointed at it is
//     deleted by the next apply (the plan's Orphans).
//
// It returns the dashboard id and the number of writes made.
func executePlan(ctx context.Context, app *App, p *dashPlan) (string, int, error) {
	writes := 0
	do := func(method, path string, body any) (any, error) {
		var b []byte
		if body != nil {
			var err error
			if b, err = json.Marshal(body); err != nil {
				return nil, err
			}
		}
		writes++
		return doJSONRequest(ctx, app, method, path, b)
	}
	tag := func(entity, id string, t tagChange) error {
		ent := map[string]string{"type": entity, "id": id}
		switch t.Op {
		case "delete":
			_, err := do(http.MethodDelete, "/tags", map[string]any{"name": t.Name, "entity": ent})
			return err
		case "update":
			_, err := do(http.MethodPut, "/tags", map[string]any{"name": t.Name, "value": t.Value, "entity": ent})
			return err
		}
		_, err := do(http.MethodPost, "/tags", map[string]any{"name": t.Name, "value": t.Value, "entity": ent})
		return err
	}

	dashID := p.Dashboard.ID
	if p.Dashboard.Action == actCreate {
		// Create, then attach: live (#198) a create body's widget_ids is
		// dropped, and so are its tags.
		body := map[string]any{"name": p.want.Name}
		if p.want.Description != nil && *p.want.Description != "" {
			body["description"] = *p.want.Description
		}
		payload, err := do(http.MethodPost, "/dashboards", body)
		if err != nil {
			return "", writes, err
		}
		m, _ := payload.(map[string]any)
		dashID, _ = m["id"].(string)
		if dashID == "" {
			return "", writes, fmt.Errorf("the dashboard create response has no id")
		}
		p.Dashboard.ID = dashID
	}
	for _, t := range p.Dashboard.Tags {
		if err := tag("dashboard", dashID, t); err != nil {
			return dashID, writes, err
		}
	}
	attach := func(widgetID string) error {
		_, err := do(http.MethodPost, "/dashboards/"+url.PathEscape(dashID)+"/widgets",
			map[string]any{"widget_ids": []string{widgetID}})
		return err
	}
	createDef := func(def apiDef) (string, error) {
		def.Description = definitionMarker(dashID)
		payload, err := do(http.MethodPost, "/metrics/definitions", def)
		if err != nil {
			return "", err
		}
		m, _ := payload.(map[string]any)
		id, _ := m["id"].(string)
		if id == "" {
			return "", fmt.Errorf("the metric definition create response has no id")
		}
		return id, nil
	}

	for i := range p.Widgets {
		ch := &p.Widgets[i]
		switch ch.Action {
		case actAttach:
			if err := attach(ch.ID); err != nil {
				return dashID, writes, err
			}
		case actDetach:
			if _, err := do(http.MethodDelete, "/dashboards/"+url.PathEscape(dashID)+"/widgets/"+url.PathEscape(ch.ID), nil); err != nil {
				return dashID, writes, err
			}
		case actCreate:
			defID, err := createDef(ch.want.Def)
			if err != nil {
				return dashID, writes, err
			}
			payload, err := do(http.MethodPost, "/widgets", widgetBody(ch.want, nil, []string{defID}))
			if err != nil {
				return dashID, writes, err
			}
			m, _ := payload.(map[string]any)
			ch.ID, _ = m["id"].(string)
			if ch.ID == "" {
				return dashID, writes, fmt.Errorf("the widget create response has no id")
			}
			if err := attach(ch.ID); err != nil {
				return dashID, writes, err
			}
			for _, t := range adoptTags(&liveWidget{}, dashID) {
				if err := tag("widget", ch.ID, t); err != nil {
					return dashID, writes, err
				}
			}
		case actUpdate:
			if ch.Adopt {
				for _, t := range adoptTags(ch.live, dashID) {
					if err := tag("widget", ch.ID, t); err != nil {
						return dashID, writes, err
					}
				}
			}
			metricIDs := ch.live.MetricIDs
			if ch.NewDefinition {
				defID, err := createDef(ch.want.Def)
				if err != nil {
					return dashID, writes, err
				}
				metricIDs = []string{defID}
			}
			if ch.UpdateWidget {
				if _, err := do(http.MethodPut, "/widgets/"+url.PathEscape(ch.ID), widgetBody(ch.want, ch.live, metricIDs)); err != nil {
					return dashID, writes, err
				}
			}
			// Only now does nothing point at the old definitions.
			for _, d := range ch.DeleteDefinitions {
				if _, err := do(http.MethodDelete, "/metrics/definitions/"+url.PathEscape(d), nil); err != nil {
					return dashID, writes, err
				}
			}
			if ch.Attach {
				if err := attach(ch.ID); err != nil {
					return dashID, writes, err
				}
			}
		case actDelete:
			for _, d := range ch.DeleteDefinitions {
				if _, err := do(http.MethodDelete, "/metrics/definitions/"+url.PathEscape(d), nil); err != nil {
					return dashID, writes, err
				}
			}
			// Deleting a widget also detaches it (live 2026-10-21).
			if _, err := do(http.MethodDelete, "/widgets/"+url.PathEscape(ch.ID), nil); err != nil {
				return dashID, writes, err
			}
		}
	}
	for _, d := range p.Orphans {
		if _, err := do(http.MethodDelete, "/metrics/definitions/"+url.PathEscape(d), nil); err != nil {
			return dashID, writes, err
		}
	}
	if p.Dashboard.Layout {
		layout, _ := desiredLayout(p.want, p.Widgets)
		// PATCH takes name or layout, never both (the spec's oneOf).
		if _, err := do(http.MethodPatch, "/dashboards/"+url.PathEscape(dashID),
			map[string]any{"layout": map[string]any{"widget_layouts": layout}}); err != nil {
			return dashID, writes, err
		}
	}
	return dashID, writes, nil
}

// widgetBody is the full create/PUT body of an inline widget. Fields the
// manifest leaves out keep the widget's current value (PUT replaces the
// whole widget); a new widget defaults to a line chart.
func widgetBody(want *wantWidget, live *liveWidget, metricIDs []string) map[string]any {
	body := map[string]any{"name": want.Name, "metric_ids": metricIDs}
	typ, desc, aux := want.Type, "", want.Aux
	if live != nil {
		desc = live.Description
		if typ == "" {
			typ = live.Type
		}
		if aux == nil {
			aux = live.Aux
		}
	}
	if want.Description != nil {
		desc = *want.Description
	}
	body["type"] = orDefault(typ, "line")
	if desc != "" { // the API takes a description of 1 to 1024 characters
		body["description"] = desc
	}
	if len(aux) > 0 {
		body["aux"] = aux
	}
	return body
}
