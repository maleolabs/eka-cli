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
