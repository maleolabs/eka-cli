# Durability — where shares live and how they travel

Shares are **workspace-native** units (`source_repo = runtime`, ADR-032): they live in the local workspace store and never enter a repository snapshot. Consequences:

- A clone of the repo on another device receives **zero** shares.
- `eka sync` does not delete them (pull upserts, push reads) — but they exist only on the device that built them.

Moving shares across devices:

```bash
# Package and import (self-contained, works without the source)
eka shr export <ns/type:id> -o <name>.ekapkg
eka shr import <name>.ekapkg        # on the other device

# Or build with adopt in one step (from the source repo)
eka shr build <source> --level L0 --adopt   # publish + adopt + push

# Or re-attribute into a repo snapshot (then normal sync carries them)
eka sync push --adopt               # adopt workspace-native units, then push
```

Loss model: deletion is immediate and permanent (no trash). Bulk filters (`--project/--level`) cross every project in the workspace — always preview with `eka shr delete <filter> --dry-run` first, then delete single explicit forms.
