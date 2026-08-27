# eka-cli

**eka-cli** is the official command-line interface for the EKA engineering
knowledge standard. It is a consumer of
[`eka-core`](https://github.com/maleolabs/eka-core) — the CLI contains no
domain logic of its own; every command delegates to an eka-core engine. It is
the official interface for bootstrapping, validating, exchanging, and running
the EKA Knowledge Runtime.

Module path: `github.com/maleolabs/eka-cli`

## Installation

### Release binaries (recommended)

Prebuilt binaries for Linux and macOS (amd64, arm64) are published on the
[GitHub releases](https://github.com/maleolabs/eka-cli/releases) page. The
installers download the binary, verify its SHA-256 checksum against the
release's `SHA256SUMS.txt` (fail-closed — an unverifiable binary is never
installed), and place it on your `PATH`.

Linux / macOS:

```sh
curl -fsSL https://github.com/maleolabs/eka-cli/releases/latest/download/install.sh | sh
```

Windows (PowerShell):

```powershell
Invoke-RestMethod https://github.com/maleolabs/eka-cli/releases/latest/download/install.ps1 | Invoke-Expression
```

Options (install.sh): `--version vX.Y.Z`, `--to <dir>`, `--completion <shell>`,
`--no-completion`.

### From source

```sh
go install github.com/maleolabs/eka-cli/cmd/eka@v1.0.0
```

## Basic usage

The complete command list, with one-line descriptions:

| Command | Description |
|---|---|
| `validate` | Validate an EKA repository against the conformance rules. |
| `init` | Bootstrap a new EKA repository. |
| `export` | Export an EKA repository to an RSF package. |
| `import` | Import an RSF package into the current repository. |
| `get` | Retrieve knowledge as machine-readable CKO JSON. |
| `context` | Construct the engineering context around a knowledge subject. |
| `view` | Project the Engineering Knowledge Model. |
| `watch` | Watch a projection live, redrawn on change. |
| `sync` | Sync a repository with the EKA workspace. |
| `project` | Manage EKA workspace projects. |
| `status` | Show the EKA workspace status. |
| `integrity` | Verify the EKA workspace integrity. |
| `update` | Update the EKA CLI to the latest release. |
| `version` | Print version information. |
| `transition` | Transition a work item, plan or container state. |
| `note` | Create a note draft (comment) on an artifact. |
| `feedback` | Report EKA feedback (draft → publish as a GitHub issue). |
| `snapshot` | Inspect and repair repository snapshots. |
| `new` | Scaffold a draft. |
| `edit` | Open a draft in the editor. |
| `draft` | Manage drafts (list / validate). |
| `publish` | Publish a draft as an immutable knowledge object. |
| `discard` | Discard a draft without publishing. |

Get help for any command with `eka help <command>` or `eka <command> --help`.

Exit codes are deterministic: `0` fully compliant (warnings allowed), `1`
blocking violations present, `2` usage or internal error.

## Plugins

eka-cli is extensible through an **executable plugin contract** (v1). A plugin
extends the CLI — for example, [`eka-mcp`](https://github.com/maleolabs/eka-mcp)
adds AI-agent integration — without the CLI depending on any plugin's
implementation.

### Installing the official `mcp` plugin

```sh
eka plugin install mcp
```

This installs the official `eka-mcp` plugin from its GitHub release, with
checksum verification (the plugin binary and its `SHA256SUMS.txt` always come
from the same release; a missing or mismatched checksum refuses the install).
The intended flow:

1. `eka` resolves the plugin identity `mcp` against the official registry.
2. The plugin binary is downloaded and checksum-verified, then placed on
   `PATH` (or `~/.eka/plugins`) as `eka-mcp`.
3. `eka` runs `eka-mcp manifest --json` to learn what the plugin provides, and
   `eka-mcp install <kind> --dir <dir> --json` to delegate artifact
   installation (skills, commands) into the agent configuration directory.

### Creating a plugin acceptable to eka-cli

A plugin is an **executable named `eka-<name>`** (e.g. `eka-mcp`) discoverable
on `PATH` or under the EKA plugin directory (`$EKA_PLUGIN_DIR`, then
`~/.eka/plugins`). The CLI talks to it through two machine-readable
subcommands; the JSON output on stdout is the contract.

**1. `eka-<name> manifest --json`** — emit the plugin self-description:

```json
{
  "contract": "v1",
  "name": "mcp",
  "version": "0.1.0",
  "description": "one-line summary",
  "artifacts": [
    { "kind": "skills", "entries": ["eka-orientation", "…"] },
    { "kind": "commands", "entries": ["eka-discuss.md", "…"] }
  ]
}
```

The `Manifest` fields are `contract` (must equal `"v1"`), `name`, `version`
(semver), `description`, and `artifacts` (the installable families, each with a
`kind` and its `entries`).

**2. `eka-<name> install <kind> --dir <dir> [--dry-run] --json`** — install
one artifact family into an agent configuration directory and emit the result:

```json
{ "installed": ["eka-orientation", "…"], "version": "0.1.0" }
```

With `--dry-run` the plugin reports the plan without touching the filesystem.

**Discovery.** `Discover` finds any `eka-*` executable on `PATH` (excluding the
CLI itself) plus any `eka-*` executable in the plugin directories; duplicate
names collapse to the first discovered path.

**Trust tiers.** Plugins from the **official registry** (the curated set of
maleolabs-published plugins, e.g. `mcp`) receive **full trust** and install
with checksum verification, no interactive prompt. **Third-party** plugins
require explicit **consent** before they are installed or executed — the user
must approve the plugin before the CLI will act on it.

Plugins import the contract types from `github.com/maleolabs/eka-core/plugin`
(`Manifest`, `Artifact`, `InstallOptions`, `InstallResult`,
`ContractVersion`) to implement their executable side; they never import the
CLI's internal packages.

## Versioning

- **Semantic versioning**, tag-driven. The CLI build version defaults to `dev`
  and is overridden at build time:

  ```sh
  go build -ldflags "-X github.com/maleolabs/eka-cli/cmd.version=v1.2.3" ./cmd/eka
  ```

- `eka version` prints the CLI build version and the **EKA standard version**
  this CLI implements — **EKA Standard 1.0**. The standard's two-component
  version axis is independent of the CLI's semver.

- **Release pipeline** — tag `vMAJOR.MINOR.PATCH` (RC suffix allowed, e.g.
  `v1.2.3-rc.1`) triggers `.github/workflows/release.yml`:
  semver regex check, `gh release view` duplicate guard, `anvil pipeline ci`
  quality gate (gofmt/vet/test) before publication, and
  `gh release create --generate-notes`. See [CONTRIBUTING.md](CONTRIBUTING.md)
  for the full release process, tag-immutability rule (pushed tags are never
  deleted/recreated), and ecosystem release order
  `eka-standard -> eka-core -> eka-cli -> eka-mcp`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Design records and ADRs live in the EKA
knowledge system, not in this repository.

## License

Apache License 2.0.
// codegraph marker
