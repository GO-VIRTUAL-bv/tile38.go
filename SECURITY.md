# Security policy

## Reporting a vulnerability

Report it privately through GitHub:
[**Security → Report a vulnerability**](https://github.com/GO-VIRTUAL-bv/tile38.go/security/advisories/new).
Private reporting is enabled on this repository, so the report stays between us
until a fix ships. Please do not open a public issue for a security problem.

Expect an acknowledgement within a week. A fix ships as a new tag, and the
advisory is published once the tag is up.

## Supported versions

Pre-1.0, only the latest minor is supported. A fix lands on `main` and goes out
in the next tag — there are no patch branches for older minors.

## Scope

This is a client library with no runtime dependencies, so the surface is what it
does with the arguments you give it and the bytes a server sends back:

- **`internal/resp`** parsing a malformed or hostile reply — a panic, an
  unbounded allocation, or a read that runs past its frame.
- **Command assembly** (`builders.go` and its siblings) letting caller input
  inject additional protocol tokens.
- **`internal/conn`** and credential handling — AUTH, and anything that could
  put a password somewhere it does not belong.

Out of scope: vulnerabilities in Tile38 itself, which belong with
[tidwall/tile38](https://github.com/tidwall/tile38/security); and the
test-only dependencies behind the `integration` build tag, which never enter a
consumer's build.
