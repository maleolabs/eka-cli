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
4. **Owner-only exception.** The project owner — git config `user.name`
   **Marij Mokoginta** or email **marijmokoginta04@gmail.com** — MAY push
   directly to `main` or open a PR from a non-`develop` branch. Everyone else
   is strictly forbidden from direct `main` pushes and from non-`develop`
   pull requests.

## Quality gate

Changes are delivered through the **anvil pipeline CI**. Run the pipeline
locally before opening a pull request:

```sh
anvil pipeline ci
```

The CI gate runs formatting, vet, and the full test suite; a failing gate
blocks merge. (Manually: `gofmt -l .`, `go vet ./...`, `go test ./...` must all
pass.)

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
