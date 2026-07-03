# devedge-cli-sdk

The open-core, mechanism-only CLI SDK for devedge — the **command-line mirror
of [`devedge-sdk`](https://github.com/infobloxopen/devedge-sdk) and
[`devedge-ufe-sdk`](https://github.com/infobloxopen/devedge-ufe-sdk)**. It is
small, public, and carries **no proprietary dependencies**.

It provides two things:

- **`clikit`** — the CLI runtime library a generated command-line tool imports.
- **`cligen`** — a generator that turns an **enriched OpenAPI v3** spec into a
  cobra "domain command module".

The generated CLI imports `clikit` exactly as an apx-generated Go client imports
`oapi-codegen/runtime` and an Angular client imports `@angular`. The runtime is
the stable seam; the generated code is disposable.

## The seam is public; the proprietary implementation binds on top privately

This repo follows the same governance principle as `devedge-sdk`: **the seam is
public; any product-specific implementation is a private binding.** In
`devedge-sdk` the authorization *seam* is the public `authz.Authorizer`
interface; a concrete decision point — say, an OPA-backed authorizer — binds to
it from a separate private package, and nothing about that engine leaks into the
public seam.

The CLI SDK works the same way. Everything here is **mechanism, not policy**:

- The **session seam** is `auth.Session`. The generic OIDC binding (RFC 8628
  device-authorization grant) is public because OIDC is a standard; a
  provider-specific binding (Okta, PDS, Auth0, ...) that merely supplies the
  issuer/client/audience is a separate **private** package. No identity provider
  is named or hardwired here.
- The **contract seam** is the enriched OpenAPI spec `cligen` consumes. The set
  of resources, fields, and behaviors is the service's own contract, not
  baked-in here.
- The **plugin seam** is git/kubectl-style `PATH` dispatch. The set of
  subcommands a shell offers is host-composed, never hardcoded.

Nothing product-specific lives in this repository — no identity-provider names,
no service catalog, no auth presets. Those bind on top, privately, in a separate
extension — the same way a private authorizer binds to `authz.Authorizer` in
`devedge-sdk`.

## Why this exists

A service in the devedge ecosystem already publishes an enriched OpenAPI
contract (P0a): native `required`/`readOnly`/`writeOnly`/`enum` plus `x-aip-*`
extensions that carry resource identity, methods, pagination, references, and
field behavior. That contract is enough to project an out-of-the-box CLI —
without hand-writing a flag for every field or re-implementing auth per tool.
`cligen` turns the contract into commands; `clikit` supplies the runtime the
commands need. A single rebrandable shell (`ib` by default) composes those
commands in-process or dispatches to external plugins.

## Packages

| Import | Purpose | Runtime deps |
|---|---|---|
| `clikit` | CLI runtime: rebrandable `App`/shell, profile `Config`, table/json/yaml output, authed HTTP transport, AIP-122 name helpers, LRO poll, `PATH` plugin dispatch. | `spf13/cobra`, `yaml.v3` |
| `clikit/auth` | The `Session` seam + `StubSession` (`--dev`) + a keychain-agnostic token store (0600 file default). | none |
| `clikit/auth/oidc` | Generic OIDC device-grant `Session` (RFC 8628). Provider-agnostic; discovers endpoints via `.well-known`. | none |
| `cmd/cligen` | Generator CLI: enriched OpenAPI v3 → cobra domain command module. | `getkin/kin-openapi` |
| `cmd/clikit-doctor` | Diagnostics binary (config + plugin + build report). | (clikit) |

The generation logic lives in `internal/gen` (unit-testable, exec-free); it is
not a public import surface.

## The load-bearing rule: the shell owns the session; generated commands never authenticate

The **shell** constructs the `auth.Session` (usually the OIDC one) exactly once
and threads it into the runtime. Generated domain commands receive only a
**read-only `clikit.Runtime`** — they can make authed requests and render
output, but they cannot construct a session or reach the identity provider. This
is the CLI mirror of the ufe rule "the shell owns the session; uFEs never
authenticate."

## Getting started

The fastest path to a working CLI is to scaffold one with the devedge CLI:

```bash
de cli add --domain widgets --spec ./openapi/widgets.openapi.yaml
```

`de cli add` runs `cligen` against a service's enriched OpenAPI spec and wires
the generated domain module into an `ib` shell. It ships in the
[`devedge`](https://github.com/infobloxopen/devedge) CLI, the same tool that
scaffolds backend services with `de new service` and micro-frontends with
`de ufe new`.

To wire a CLI by hand instead, follow the quickstart below.

## Quickstart

### Generate a domain command module

```bash
go run github.com/infobloxopen/devedge-cli-sdk/cmd/cligen \
  --input ./openapi/widgets.openapi.yaml \
  --output ./gen/widgets \
  --module github.com/acme/ib \
  --domain widgets --package widgets --app ib
```

### Compose it into a rebrandable shell (the shell owns the session)

```go
package main

import (
    "fmt"
    "os"
    "path/filepath"

    "github.com/infobloxopen/devedge-cli-sdk/clikit"
    "github.com/infobloxopen/devedge-cli-sdk/clikit/auth"
    "github.com/infobloxopen/devedge-cli-sdk/clikit/auth/oidc"
    widgets "github.com/acme/ib/gen/widgets"
)

func main() {
    app := clikit.NewApp("ib")
    // The shell owns OIDC. --dev bypasses it with a static stub token.
    app.SetSessionFactory(func(p clikit.Profile) (auth.Session, error) {
        return oidc.New(oidc.Config{
            Issuer:   p.Auth.OIDC.Issuer,
            ClientID: p.Auth.OIDC.ClientID,
            Store:    &auth.FileStore{Path: filepath.Join(clikit.ConfigDir("ib"), "token.json")},
        }), nil
    })
    app.Root().AddCommand(widgets.Command(app.Runtime()))
    if err := app.Execute(); err != nil { // builtin subcommands, then PATH plugins
        fmt.Fprintln(os.Stderr, "error:", err)
        os.Exit(1)
    }
}
```

```console
$ ib widgets --help
$ ib widgets list -o json
$ ib widgets get widgets/abc123
$ ib widgets create --display-name "Gadget" --category standard --secret-token-stdin
$ ib --dev widgets list        # static dev token, no real IdP
```

See [`examples/ib`](examples/ib) for a complete, buildable shell with a
committed generated module and an end-to-end test that drives a fake service
through the runtime.

## What this fixes

1. **Hand-written CLIs per service** — `cligen` projects the commands from the
   service's own enriched OpenAPI contract; no per-field flag wiring.
2. **Field behavior lost at the CLI** — `REQUIRED` becomes a required flag,
   `OUTPUT_ONLY` fields are never input flags, `IMMUTABLE` is excluded from
   update, `enum` values are validated, and `INPUT_ONLY`/secret material is read
   from stdin and never echoed.
3. **Auth re-implemented per tool** — one generic OIDC device-grant `Session`
   seam; the shell owns it, commands consume the read-only runtime.
4. **Bearer/401 handling copy-pasted** — the authed transport attaches the
   bearer and retries once on 401 (login → retry), the Go analog of the ufe
   `createAuthedFetch`.
5. **Proprietary lock-in** — none. Every seam is a standard mechanism;
   product-specific bindings (an OIDC issuer, an auth preset) live in separate
   private packages, the same way a private authorizer binds to
   `authz.Authorizer` in `devedge-sdk`.

## Development

```sh
go build ./...
go vet ./...
go test ./...
gofmt -l .
```

Go 1.23+. Dependencies are kept light: `cobra`, `kin-openapi`, `yaml`. No
dependency on `devedge-sdk` or `apx`.

## License

[Apache-2.0](./LICENSE).
