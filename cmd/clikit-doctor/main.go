// Command clikit-doctor reports the CLI environment and configuration health.
// It is a thin wrapper over clikit.NewDoctorCommand so a shipped shell and the
// standalone binary run the same checks.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/infobloxopen/devedge-cli-sdk/clikit"
)

func main() {
	app := flag.String("app", "clikit", "application name to diagnose")
	flag.Parse()

	cmd := clikit.NewDoctorCommand(*app)
	cmd.SetArgs([]string{}) // empty (not nil) so cobra does not fall back to os.Args
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
