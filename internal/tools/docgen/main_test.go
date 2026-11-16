package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/bronto-community/bronto-cli/internal/cli"
)

func TestCheckIconsCoversTheCommandTree(t *testing.T) {
	root := cli.NewRootCmd()
	root.InitDefaultCompletionCmd()
	if err := checkIcons(root); err != nil {
		t.Fatal(err)
	}
}

func TestCheckIconsRejectsMissingAndStaleEntries(t *testing.T) {
	root := &cobra.Command{Use: "bronto"}
	root.AddCommand(&cobra.Command{Use: "brand-new", Run: func(*cobra.Command, []string) {}})
	err := checkIcons(root)
	if err == nil {
		t.Fatal("checkIcons accepted a command without an icon")
	}
	for _, want := range []string{
		`"brand-new" has no icon`,
		`commandIcons: "search" is not a documented top-level command`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
