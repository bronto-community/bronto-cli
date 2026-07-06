package main

import (
	"os"

	"github.com/svrnm/bronto-cli/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	cmd := cli.NewRootCmd()
	if err := cmd.Execute(); err != nil {
		// Error rendering is wired properly in Task 2.
		os.Stderr.WriteString(err.Error() + "\n")
		return 1
	}
	return 0
}
