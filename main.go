// Command mmb is the monstermailbox CLI for agents.
//
// Reads MONSTERMAILBOX_API_KEY from the environment and submits
// authenticated requests to the monstermailbox API. The full
// command tree lives under cmd/.
package main

import (
	"fmt"
	"os"

	"github.com/theinventor/monstermailbox-cli/cmd"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mmb:", err)
		os.Exit(1)
	}
}
