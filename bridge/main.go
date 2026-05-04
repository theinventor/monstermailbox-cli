// Command mmb-bridge is the local Gmail-to-monstermailbox bridge
// daemon. It runs on the user's machine, watches Gmail via the
// gogcli-managed Pub/Sub watch, applies a whitelist, and forwards
// matched messages to the user's monstermailbox tenant.
//
// Distributed alongside `mmb` from the public theinventor/monstermailbox-cli
// repo. Build with `go build -o mmb-bridge ./bridge`.
package main

import (
	"fmt"
	"os"

	"github.com/theinventor/monstermailbox-cli/bridge/cmd"
)

func main() {
	if err := cmd.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
