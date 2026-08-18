---
name: release
description: Cut a tagged release of this module — verify, tag, push, and confirm the Release workflow published it.
disable-model-invocation: true
---

# Cutting a release

For a Go module the tag *is* the release: once `proxy.golang.org` has served a
version, its checksum is immutable and cannot be corrected. Retagging a version
that anyone has fetched gives them a `SECURITY ERROR: checksum mismatch`, which
looks like an attack rather than a mistake. So the order below matters — every
check happens before the tag is pushed, not after.

## 1. The tree must be releasable

```bash
git switch main && git pull --ff-only
git status --porcelain          # must be empty
make lint                       # both tag sets
make test-all                   # unit + integration, needs Docker
```

A dirty tree is the most common cause of tagging something that was never
tested. Do not continue past a non-empty `git status`.

## 2. Check the pinned server is still current

`.version` pins the `tile38/tile38:edge` digest the integration tests ran
against. If upstream has moved, the suite just certified an old server:

```bash
cat .version
docker buildx imagetools inspect tile38/tile38:edge --format '{{.Manifest.Digest}}'
```

If they differ, bump `.version`, re-run `make test-integration`, and land that as
its own change before releasing. The `Tile38 Upstream Check` workflow opens an
issue when this drifts, so there may already be one open.

## 3. Check what the release breaks

Run the `api-compat-reviewer` agent against the last tag. This module is public,
so removed or renamed exported identifiers break consumers you cannot see. The
result decides the version number and belongs in the release notes.

## 4. Pick the version

Semver, and the module is `v0.x`:

- breaking change → bump the **minor** (`v0.1.0` → `v0.2.0`); permitted pre-1.0,
  but it must be deliberate and stated in the notes
- additive or fixes only → bump the **patch**

Never reuse a version number. Check what already exists:

```bash
git tag --sort=-v:refname | head -5
gh release list --limit 5
```

## 5. Tag and push

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

Pushing the tag triggers `.github/workflows/release.yml`, which re-runs the unit
and integration suites and only then publishes the release with generated notes.
A tag whose workflow fails has not released anything — but the tag still exists,
so fix forward with a new patch version rather than deleting and retagging.

## 6. Confirm it published

```bash
gh run list --workflow=Release --limit 1
gh release view vX.Y.Z
```

## If something goes wrong before anyone fetched it

Deleting a tag is only safe while no one has consumed it — which for a public
module means almost immediately after pushing, and never once
`proxy.golang.org` has cached it. Check first:

```bash
curl -s "https://proxy.golang.org/github.com/!g!o-!v!i!r!t!u!a!l-bv/tile38.go/@v/list"
```

If the version appears there, it is permanent. Ship a new patch instead.
