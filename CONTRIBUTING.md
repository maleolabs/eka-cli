# Contributing

Thank you for contributing to **eka-cli**, the official EKA command-line
interface. These rules keep the repository safe, reviewable, and traceable.

## Branching model

Two long-lived branches:

| Branch | Purpose |
|---|---|
| `main` | Stable / release. Only ever updated from `develop` via a pull request. |
| `develop` | Development. All work happens here or on branches cut from here. |

## Development workflow

1. **Always branch from `develop`.** All implementation work MUST be done on a
   new branch created from `develop` — `feature/*`, `fix/*`, `refactor/*`,
   `docs/*`, or `chore/*` — never directly on `main`.
2. **Optional worktree.** Heavy work MAY be done in a separate git worktree so
   the primary worktree stays isolated.
3. **Merge to `main` via PR from `develop`.** Merging to `main` MUST come from
   the `develop` branch through a GitHub pull request.

## Quality gate

Changes are delivered through the **anvil pipeline CI**. Run the pipeline
locally before opening a pull request:

```sh
anvil pipeline ci
```

The CI gate runs formatting, vet, and the full test suite; a failing gate
blocks merge. (Manually: `gofmt -l .`, `go vet ./...`, `go test ./...` must all
pass.)

## Release process

Releases are **tag-driven semver** and fully automated by
`.github/workflows/release.yml`.

### Trigger

- Push a tag `vMAJOR.MINOR.PATCH` (RC suffix allowed: `v1.2.3-rc.1`,
  `-alpha`, `-beta`). The workflow also supports `workflow_dispatch` with a
  `version` input for re-runs without deleting the tag.

### Guardrails (fail-fast, before any build)

1. **Semver regex check** — `^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$` anchored;
   non-matching versions abort.
2. **Version parity** — tag version must equal `anvil.yaml` `version` (bump via
   `scripts/bump.sh` before tagging).
3. **Duplicate-release check** — `gh release view v$VERSION` must fail; an
   existing release aborts the run.

### Quality gate inside the release

The workflow runs `anvil pipeline ci` (gofmt, `go vet`, `go test` incl.
`-race`) **before** any artifact is built or published. A failing gate blocks
the release.

### Publication

After the gate, the workflow builds platform binaries, packages the source
artifact, generates `SHA256SUMS.txt`, and creates the GitHub Release:

```sh
gh release create "v$VERSION" --title "v$VERSION" --generate-notes --target "$GITHUB_SHA"
```

`--generate-notes` auto-populates release notes; `prerelease` is inferred from
`-alpha`/`-beta`/`-rc` suffixes.

### Tag immutability

**Pushed tags are never deleted or recreated.** If a tag is wrong, bump to a
new patch/minor version and push a new tag. The workflow never force-pushes
tags; `workflow_dispatch` exists precisely so a release can be re-triggered
without moving a tag. Deleting and re-pushing `v*` breaks Go module
resolution (`go.sum` + sumdb) and the `git describe` version stamp.

### Ecosystem release order (one-way)

The EKA repos form a dependency chain — releases flow one way:

```
eka-standard -> eka-core -> eka-cli -> eka-mcp
```

- `eka-standard` is the normative spec (no code).
- `eka-core` is the Go library (integrity via `go.sum` + Go checksum DB; no
  binaries/checksums — `go get` verifies it).
- `eka-cli` and `eka-mcp` are CLIs/plugins with binaries + `SHA256SUMS.txt`.

Release in order. When cutting a coordinated wave, bump and tag
`eka-core` first, then `eka-cli` (which pins the new core), then
`eka-mcp`. `eka-cli`'s `release.yml` is the reference implementation;
`eka-core` adapts it for a library (same triggers, semver, duplicate-guard,
and quality gate, but no binary build/checksum steps).

See `scripts/bump.sh` (tag, push, and trigger) and `anvil.yaml` (`version`).

### Consumer pre-release validation (upstream-release-exists)

Every consumer release — `eka-cli` on `eka-core`, `eka-mcp` on
`eka-cli`/`eka-core` — MUST prove the upstream release it pins exists
before tagging. This prevents a consumer from silently building against a
stale pseudo-version.

Checklist — include explicitly in every consumer release work item:

1. **Upstream release exists** — `gh release view vX.Y.Z --repo maleolabs/eka-core`
   (or `--repo maleolabs/eka-cli`, or the `eka-standard` release asset) must
   succeed. A 404 means the upstream has not been published — stop and publish
   it first.
2. **Imported-constant / asset check** — verify the consumer will build against
   the published upstream, not a stale pin:
   - `go list -m github.com/maleolabs/eka-core` shows `vX.Y.Z` (not
     `v0.0.0-...` pseudo-version).
   - `eka version --json` axes or the vendored `EKA` file (via
     `standardembed`) matches the upstream version you expect.
3. **No pseudo-version surprise** — `go.mod` must pin an exact published tag,
   never a commit hash. The `go get` bump below guarantees this.

### Go dependency bump (go get)

After the upstream release is verified, bump the consumer's `go.mod`
explicitly:

```sh
# from the consumer repo (e.g. eka-cli bumping to eka-core v1.2.3)
go get github.com/maleolabs/eka-core@v1.2.3
go mod tidy
go vet ./...
git add go.mod go.sum
git commit -m "chore: bump eka-core to v1.2.3"
```

For a coordinated wave, repeat for each downstream repo before tagging it
(`eka-mcp` similarly runs `go get github.com/maleolabs/eka-cli@v...` /
`eka-core@v...` as needed). The bump commit lands on `develop` before the
tag is cut, so the tagged build and every `go get` consumer resolve the
same published module.

### Go module rules

- **Never delete or recreate a pushed `v*` tag.** The tag-immutability rule
  above is a Go modules requirement: the Go checksum DB and downstream
  `go.sum` entries are anchored to the tag's commit. Re-pushing breaks
  `go get` resolution.
- **Use `-rc` for trials.** Pre-releases carry a suffix — `v1.3.0-rc.1`,
  `v1.3.0-alpha.1`, `v1.3.0-beta.1`. The suffix makes `go get` and
  `gh release view` distinguish stable from trial builds and sets the
  `prerelease` flag in the workflow.
- **No major bump without `/v2` path.** Publishing `v2.0.0` requires changing
  the module path to `github.com/maleolabs/eka-cli/v2` (and all imports).
  Do not publish `v2` on the `v1` path.

> Note: the canonical `eka-core` release docs live in the `eka-core`
> repository. This section is the `eka-cli`-local operationalization of the
> same one-way order; it is kept in sync with that repo's
> `CONTRIBUTING.md`/`README.md` (deferred side — same pattern as
> `sto:release-core-pipeline`).

## Design records

Architecture decisions, design records, and ADRs are Engineering Knowledge and
live in the EKA knowledge system — not in this repository. Record significant
decisions there, not as free-floating docs.

## Language and style

- Code and doc comments are written in English.
- The CLI contains no domain logic: keep `cmd/` a thin layer that delegates to
  `eka-core` engines, and do not import the Runtime Kernel's private packages
  (`store`, `workspace`, `sync`, `compile`) in production code.
- Keep command output deterministic: the same input must always produce the
  same bytes (plain text when piped, colored only on a TTY).
