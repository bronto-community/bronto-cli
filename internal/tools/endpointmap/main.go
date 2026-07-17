// Command endpointmap prints the CLI's management-API endpoint inventory
// (see cli.EndpointInventory) as JSON. spec-sync runs it and feeds the
// output to scripts/spec-digest.sh, which uses it to classify upstream
// spec changes by CLI impact: new endpoints without coverage, removed
// endpoints the CLI depends on, modified endpoints backing commands.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bronto-community/bronto-cli/internal/cli"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cli.EndpointInventory()); err != nil {
		fmt.Fprintln(os.Stderr, "endpointmap:", err)
		os.Exit(1)
	}
}
