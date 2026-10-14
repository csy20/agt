# Changelog

## 0.1.0

Local reviewable beta. Not published. Not an external beta for macOS, Windows, or linux/arm64.

This repository has no `LICENSE` file and is not presented as licensed.

- Fail closed when `AGT_INTEGRATION=1` and Postgres, Docker, or a sandbox image is missing. `make unit` stays dependency-free. `scripts/test.ps1` is the PowerShell entry and was not executed here.
- Score 20 scripted fake-provider repairs across Python, Go, and JavaScript, including a recovery, an approved test-file edit that passes the in-repo test and fails a hidden check, and an honest verification failure. This is not a hosted-model evaluation.
- `scripts/package.sh` writes unsigned `agt` and `agtd` archives, checksums, and `dist/BUILD.txt`. linux/amd64 is executed only as `agt version`. The other archives are cross-compiled and were not executed.

The earlier milestones on this line of work added loopback auth, explicit Python/Go/JavaScript profiles, a persisted repair transcript, a digest-bound local apply, job leases, and daemon discovery. A hosted model was not called. Applying a patch remains explicit. A cost budget is not a hard dollar cap when prices are zero or usage is unreported. Tool execution is not exactly-once.
