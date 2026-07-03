package clikit

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

// NewDoctorCommand returns a cobra command that reports the CLI environment for
// appName: config location + presence, discovered plugins, and build info. It
// is the mechanism a shipped shell exposes as "<app> doctor", and the
// standalone clikit-doctor binary wraps it.
func NewDoctorCommand(appName string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report CLI environment and configuration health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "app:        %s\n", appName)
			fmt.Fprintf(out, "go:         %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

			dir := ConfigDir(appName)
			cfgPath := filepath.Join(dir, "config.yaml")
			fmt.Fprintf(out, "config dir: %s\n", dir)
			if _, err := os.Stat(cfgPath); err == nil {
				fmt.Fprintf(out, "config:     %s (present)\n", cfgPath)
				cfg, err := LoadConfig(appName, cfgPath)
				if err != nil {
					fmt.Fprintf(out, "  WARN: config unreadable: %v\n", err)
				} else {
					fmt.Fprintf(out, "  profiles: %d (current: %q)\n", len(cfg.Profiles), cfg.CurrentProfile)
				}
			} else {
				fmt.Fprintf(out, "config:     %s (absent)\n", cfgPath)
			}

			plugins := ListPlugins(appName)
			fmt.Fprintf(out, "plugins:    %d discovered on PATH\n", len(plugins))
			for _, p := range plugins {
				fmt.Fprintf(out, "  %s -> %s\n", p.Name, p.Path)
			}
			return nil
		},
	}
}
