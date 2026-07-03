# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `clikit` — the CLI runtime library: rebrandable `App`/`NewApp` shell,
  profile-based `Config` (XDG + env + flags), table/json/yaml `Printer`, an
  authed `http.RoundTripper` (bearer + 401→login→retry), AIP-122 resource-name
  helpers, a generic long-running-operation poll, and git/kubectl-style `PATH`
  plugin dispatch.
- `clikit/auth` — the `Session` seam, a `--dev` `StubSession`, and a
  keychain-agnostic `TokenStore` with a `0600` file default.
- `clikit/auth/oidc` — a generic OIDC `Session` over the RFC 8628 Device
  Authorization Grant, with `.well-known` discovery, single-in-flight refresh,
  and a 30-second freshness skew. Provider-agnostic; no IdP hardwired.
- `cmd/cligen` — a generator that turns an enriched OpenAPI v3 spec into a cobra
  domain command module that imports `clikit`. Honors `field_behavior`
  (`REQUIRED`, `OUTPUT_ONLY`, `IMMUTABLE`, `INPUT_ONLY`/secret) and `enum`.
- `cmd/clikit-doctor` — an environment/configuration diagnostics binary.
- `examples/ib` — a rebrandable `ib` shell composing a committed generated
  `widgets` module, with an end-to-end test that drives a fake service through
  the runtime.
