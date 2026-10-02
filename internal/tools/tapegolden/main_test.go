package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSettled(t *testing.T) {
	r := "\n" + rule + "\n"
	raw := strings.Join([]string{
		"",                             // before the first prompt
		">",                            // prompt
		"> bronto datasets li",         // typing (racy)
		"> bronto datasets list",       // typed
		"> bronto datasets list\nNAME", // output streaming (racy)
		"> bronto datasets list\nNAME\nprod\n>   ", // settled
		"> bronto datasets list\nNAME\nprod\n>",    // duplicate after trim
		">",                                        // Ctrl+L
		"> bronto ping",                            // scene 2
		"> bronto ping\nOK\n>",                     // settled, end of tape
	}, r)
	got := Settled(raw)
	want := []string{"> bronto datasets list\nNAME\nprod\n>", "> bronto ping\nOK\n>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Settled = %q\nwant %q", got, want)
	}
}

func TestUnsettled(t *testing.T) {
	frames := []string{
		"> bronto datasets list\nNAME\nprod\n>",       // settled
		"> bronto tail\nevent\n^C>",                   // interrupted, still at a prompt
		"> bronto users list\nEMAIL\nada@example.com", // scrolled: prompt lost below the screen
	}
	if got, want := Unsettled(frames), []int{2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Unsettled = %v, want %v", got, want)
	}
}
