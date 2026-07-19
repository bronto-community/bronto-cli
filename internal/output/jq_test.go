package output

import (
	"encoding/json"
	"math/big"
	"testing"
)

func TestNormalizeForJQ(t *testing.T) {
	in := map[string]any{
		"small": json.Number("42"),
		"big":   json.Number("4367602734065516544"),
		"huge":  json.Number("99999999999999999999999999"),
		"frac":  json.Number("1.5"),
		"nest":  []any{json.Number("7")},
		"rows":  []map[string]any{{"n": json.Number("8")}},
		"str":   "s",
	}
	out := normalizeForJQ(in).(map[string]any)
	if out["small"] != 42 {
		t.Fatalf("small = %#v", out["small"])
	}
	if out["big"] != int(4367602734065516544) {
		t.Fatalf("big = %#v", out["big"])
	}
	if _, ok := out["huge"].(*big.Int); !ok {
		t.Fatalf("huge = %#v, want *big.Int", out["huge"])
	}
	if out["frac"] != 1.5 {
		t.Fatalf("frac = %#v", out["frac"])
	}
	if out["nest"].([]any)[0] != 7 {
		t.Fatalf("nest = %#v", out["nest"])
	}
	if out["rows"].([]any)[0].(map[string]any)["n"] != 8 {
		t.Fatalf("rows = %#v", out["rows"])
	}
	if out["str"] != "s" {
		t.Fatalf("str = %#v", out["str"])
	}
}
