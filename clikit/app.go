// Package clikit is the CLI runtime library that a generated devedge domain
// command module imports — exactly as an apx-generated Go client imports
// oapi-codegen/runtime and an Angular client imports @angular. It is the CLI
// mirror of devedge-sdk and devedge-ufe-sdk: small, public, mechanism-only.
//
// clikit provides the rebrandable shell ([App]/[NewApp]), profile-based
// [Config], table/json/yaml [Printer] output, an authed HTTP transport that
// binds to the clikit/auth [auth.Session] seam, AIP-122 resource-name helpers,
// a generic LRO poll, and git/kubectl-style [Plugin] dispatch. Generated
// commands receive a [Runtime] and never touch this plumbing directly.
package clikit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/infobloxopen/devedge-cli-sdk/clikit/auth"
	"github.com/spf13/cobra"
)

// Runtime is the handle a generated domain command module consumes. Its methods
// resolve lazily: the concrete implementation reads config/session state that
// the [App]'s persistent pre-run populates, so a command built at tree-assembly
// time sees resolved values when it actually runs.
type Runtime interface {
	// Context returns the invocation context.
	Context() context.Context
	// Client returns an *http.Client whose transport attaches the session
	// bearer token and retries once on 401.
	Client() *http.Client
	// BaseURL returns the active profile's server base URL, or an error when
	// none is configured.
	BaseURL() (string, error)
	// Printer renders results in the format selected by --output.
	Printer() *Printer
	// AppName returns the rebranded application name (e.g. "ib").
	AppName() string
}

// SessionFactory builds an [auth.Session] for the active [Profile]. The shell
// supplies it to bind a concrete auth implementation (e.g. the generic OIDC
// device-grant provider, or a private preset) on top of the public seam. When
// nil, only the --dev stub path is available.
type SessionFactory func(Profile) (auth.Session, error)

// App is a rebrandable CLI shell. It owns the root cobra command, the global
// flags, and the resolved runtime state. The same App composes generated domain
// modules in-process or dispatches to external plugins.
type App struct {
	name           string
	root           *cobra.Command
	sessionFactory SessionFactory
	out            io.Writer

	// global flag values
	configPath string
	profile    string
	server     string
	output     string
	dev        bool
	devToken   string

	// resolved during persistent pre-run
	cfg     Config
	prof    Profile
	session auth.Session
	client  *http.Client
	format  Format
	ctx     context.Context
}

// NewApp returns a rebrandable shell named appName with a root command that
// carries the global flags and a persistent pre-run that resolves config, the
// active profile, the session, and an authed HTTP client.
func NewApp(name string) *App {
	a := &App{name: name, out: os.Stdout, ctx: context.Background()}
	a.root = &cobra.Command{
		Use:           name,
		Short:         fmt.Sprintf("%s command-line interface", name),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	a.BindGlobals(a.root)
	return a
}

// Root returns the root cobra command so the shell can add generated domain
// commands (e.g. root.AddCommand(widgets.Command(app.Runtime()))).
func (a *App) Root() *cobra.Command { return a.root }

// Runtime returns the [Runtime] handle passed to generated domain commands.
func (a *App) Runtime() Runtime { return a }

// AppName returns the rebranded application name.
func (a *App) AppName() string { return a.name }

// SetSessionFactory binds how the shell constructs a session for the active
// profile (e.g. an OIDC device-grant provider). This is the CLI analog of the
// ufe shell owning session construction.
func (a *App) SetSessionFactory(f SessionFactory) { a.sessionFactory = f }

// SetOut redirects command output (used by tests). It updates both the printer
// sink and the root command's output stream.
func (a *App) SetOut(w io.Writer) {
	a.out = w
	a.root.SetOut(w)
}

// BindGlobals registers the global flags on cmd and installs the persistent
// pre-run that resolves runtime state. NewApp calls it on the root; a generated
// plugin main calls it to run a single domain command as its own root.
func (a *App) BindGlobals(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.StringVar(&a.configPath, "config", "", "path to config file (default: $XDG_CONFIG_HOME/"+a.name+"/config.yaml)")
	f.StringVar(&a.profile, "profile", "", "named profile to use")
	f.StringVar(&a.server, "server", "", "server base URL (overrides the profile)")
	f.StringVarP(&a.output, "output", "o", "table", "output format: table, json, or yaml")
	f.BoolVar(&a.dev, "dev", false, "use a static dev token instead of real auth (development only)")
	f.StringVar(&a.devToken, "dev-token", "", "token for --dev (default: a stub token)")

	prev := cmd.PersistentPreRunE
	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		if prev != nil {
			if err := prev(c, args); err != nil {
				return err
			}
		}
		return a.resolve(c)
	}
}

// resolve loads config + profile + session and builds the authed client. It
// runs before any leaf command's RunE.
func (a *App) resolve(cmd *cobra.Command) error {
	a.ctx = cmd.Context()
	if a.ctx == nil {
		a.ctx = context.Background()
	}

	format, err := ParseFormat(a.output)
	if err != nil {
		return err
	}
	a.format = format

	cfg, err := LoadConfig(a.name, a.configPath)
	if err != nil {
		return err
	}
	a.cfg = cfg

	prof, err := cfg.ResolveProfile(a.name, a.profile, a.server)
	if err != nil {
		return err
	}
	a.prof = prof

	switch {
	case a.dev:
		a.session = auth.NewStubSession(a.devToken)
	case a.sessionFactory != nil:
		s, err := a.sessionFactory(prof)
		if err != nil {
			return err
		}
		a.session = s
	default:
		a.session = nil // unauthenticated client; real calls that need auth will fail loudly server-side
	}

	if a.session != nil {
		a.client = NewAuthClient(a.session)
	} else {
		a.client = &http.Client{}
	}
	return nil
}

// Context implements [Runtime].
func (a *App) Context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// Client implements [Runtime].
func (a *App) Client() *http.Client {
	if a.client == nil {
		return &http.Client{}
	}
	return a.client
}

// BaseURL implements [Runtime].
func (a *App) BaseURL() (string, error) {
	if a.prof.Server == "" {
		return "", fmt.Errorf("no server configured: set a profile server or pass --server")
	}
	return a.prof.Server, nil
}

// Printer implements [Runtime].
func (a *App) Printer() *Printer {
	format := a.format
	if format == "" {
		format = FormatTable
	}
	out := a.out
	if out == nil {
		out = os.Stdout
	}
	return &Printer{Out: out, Format: format}
}

// Session returns the resolved session (nil when unauthenticated). Useful for
// login/logout subcommands a shell may add.
func (a *App) Session() auth.Session { return a.session }

// Execute runs the shell against os.Args, dispatching to a builtin subcommand
// when one matches and otherwise to an external plugin (git/kubectl model).
func (a *App) Execute() error { return a.ExecuteArgs(os.Args[1:]) }

// ExecuteArgs is [App.Execute] with explicit args (used by tests). Builtin
// subcommands take priority; only when none matches does it try plugin
// dispatch, falling back to cobra so an unknown command still reports loudly.
func (a *App) ExecuteArgs(args []string) error {
	if cmd, _, err := a.root.Find(args); err != nil || cmd == a.root {
		if path, pargs, ok := LookupPlugin(a.name, args); ok {
			return ExecPlugin(path, pargs)
		}
	}
	a.root.SetArgs(args)
	return a.root.Execute()
}
