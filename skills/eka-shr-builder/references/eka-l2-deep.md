# EKA L2 deep scan

EKA L2 includes `deepDocs` (api contracts) via `deepScanEkaDocsForL2()`:
- Scans `docs/**/*.md`, `spec/**/*.md`, `api/**/*.md` from current repo (any EKA project)
- Sorted, cap 50, guard 1 MiB
- Added to `content["deepDocs"]` alongside `snapshot` (KMS)
- For any project with eka.yaml, not just project eka
