# Audit levels (non-EKA)

L0: `auditNonEKAPathLevel(root,"L0")` — file list only, 500 cap.
L1/L2: deep — docs + codegraph + redaction, 1000 cap, 1MiB guard, hash per level.
Redaction: skip `.env`, `secret`, `.pem`.
