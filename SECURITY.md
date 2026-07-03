# Security

## Reporting

Report suspected vulnerabilities via a private security advisory on
[the repository](https://github.com/infobloxopen/devedge-cli-sdk/security/advisories),
not a public issue.

## Runtime posture

- **Token scoping.** The bearer token is attached by the authed transport
  (`clikit.NewAuthClient`) to requests the CLI makes against the configured
  server base URL. Point a profile only at servers you trust with the token.
- **Token storage.** The default token store is a `0600` file
  (`auth.FileStore`) under the app's config directory. The store interface is
  keychain-agnostic: an OS-keychain binding can replace the file store without
  changing callers.
- **Write-only material.** Fields marked `INPUT_ONLY`/secret in the contract are
  read from stdin (`--<field>-stdin`) and never accepted as an echoed flag
  value, so secrets do not land in shell history or process listings.
- **Dev-only auth.** `--dev` (`auth.StubSession`) performs no authentication and
  logs a warning on construction; it must never be used against production.

## OIDC binding

- The OIDC provider uses the RFC 8628 Device Authorization Grant. It honors
  `authorization_pending` / `slow_down` / `interval` when polling the token
  endpoint and refreshes with a 30-second skew before expiry.
- Endpoints are taken from configuration or discovered from the issuer's
  `.well-known/openid-configuration`. No identity provider is hardwired; a
  provider-specific preset is a separate private package.
- ID-token claims are decoded for display only and are **not** signature-checked
  here — the resource server verifies the access token.

## Dependency posture

Dependencies are limited to `spf13/cobra`, `getkin/kin-openapi`, and
`gopkg.in/yaml.v3` (plus their transitive deps). There is no dependency on
`devedge-sdk` or `apx`, so this SDK does not widen a consumer's dependency graph
with server-side machinery.
