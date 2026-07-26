package coerce

import (
	"encoding/json"
	"testing"
)

func TestNumber(t *testing.T) {
	cases := []struct {
		v      any
		want   float64
		wantOK bool
	}{
		{float64(3.5), 3.5, true},
		{json.Number("42"), 42, true},
		{json.Number("nope"), 0, false},
		{int64(7), 7, true},
		{int(9), 9, true},
		{"5", 0, false}, // Number does NOT parse strings
		{true, 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := Number(c.v)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Number(%#v) = %v,%v; want %v,%v", c.v, got, ok, c.want, c.wantOK)
		}
	}
}

func TestNumberOrParse(t *testing.T) {
	cases := []struct {
		v      any
		want   float64
		wantOK bool
	}{
		{"5", 5, true},        // string IS parsed here
		{"1.5e3", 1500, true}, // full float syntax
		{"abc", 0, false},
		{float64(2), 2, true}, // still handles the Number cases
		{json.Number("8"), 8, true},
		{true, 0, false},
	}
	for _, c := range cases {
		got, ok := NumberOrParse(c.v)
		if got != c.want || ok != c.wantOK {
			t.Errorf("NumberOrParse(%#v) = %v,%v; want %v,%v", c.v, got, ok, c.want, c.wantOK)
		}
	}
}
