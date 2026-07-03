package clikit

import (
	"os"
	"os/exec"
	"strings"
)

// Plugin describes an external subcommand executable discovered on $PATH.
type Plugin struct {
	// Name is the subcommand path as the user would type it, e.g. "foo bar".
	Name string
	// Path is the absolute executable path (e.g. .../ib-foo-bar).
	Path string
}

// LookupPlugin finds an external subcommand for appName given the invocation
// args, using git/kubectl-style longest-prefix matching: for args
// [a b c] it probes <app>-a-b-c, then <app>-a-b, then <app>-a, returning the
// first executable found on $PATH plus the remaining args to hand it.
//
// Only leading non-flag tokens form the candidate name; the first flag stops
// the prefix. Returns ok=false when no matching executable exists.
func LookupPlugin(appName string, args []string) (path string, pluginArgs []string, ok bool) {
	var parts []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		parts = append(parts, a)
	}
	for n := len(parts); n >= 1; n-- {
		bin := appName + "-" + strings.Join(parts[:n], "-")
		if p, err := exec.LookPath(bin); err == nil {
			return p, args[n:], true
		}
	}
	return "", nil, false
}

// ListPlugins returns all executables on $PATH whose base name begins with
// "<appName>-", i.e. the external subcommands available to the shell.
func ListPlugins(appName string) []Plugin {
	prefix := appName + "-"
	seen := map[string]bool{}
	var out []Plugin
	for _, dir := range filepathListEnv("PATH") {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, prefix) || seen[name] {
				continue
			}
			full := dir + string(os.PathSeparator) + name
			if !isExecutable(full) {
				continue
			}
			seen[name] = true
			out = append(out, Plugin{
				Name: strings.ReplaceAll(strings.TrimPrefix(name, prefix), "-", " "),
				Path: full,
			})
		}
	}
	return out
}

// ExecPlugin runs the plugin at path with pluginArgs, forwarding this process's
// stdio and environment, and returns its error (including a non-zero exit as an
// *exec.ExitError).
func ExecPlugin(path string, pluginArgs []string) error {
	cmd := exec.Command(path, pluginArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

func filepathListEnv(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	return strings.Split(v, string(os.PathListSeparator))
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}
