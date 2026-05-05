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
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mmb:", err)
		// Exit code is derived from any *exitcode.Error in the error chain;
		// otherwise falls back to 1 (Generic). The taxonomy is documented
		// in internal/exitcode and surfaced via `mmb agent-context`.
		os.Exit(exitcode.ExitCodeFor(err))
	}
}
