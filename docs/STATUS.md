# AGT status

Updated 14 October 2026 on branch `milestone-1-correctness`, based on `main` at `da548377209272d05c8bc93a7787c6e5c0469bed`.
This file records what the code and the checks actually show. A passing fake-provider demo is not a live-model repair. A cross-compile is not native execution. A skipped integration test is not support.

## Milestone 1

Implemented on this branch.

| Defect | Where | Check |
|---|---|---|
| `agt approve RUN COMMAND --args-hash HASH` was rejected because `flag.Parse` stops at the first positional | `internal/cli/args.go`, `internal/cli/cli.go` | `TestApproveAcceptsFlagsAfterIDs` posts that command to an HTTP server and checks the approval body |
| Relative repo and script paths were sent unchanged | `internal/cli/paths.go` | `TestFixSendsAbsolutePathsFromAnotherDirectory` resolves them from a client directory other than the daemon directory |
| Daemon lookup used the name `agtd` only | `internal/cli/daemon.go`, `proc_unix.go`, `proc_windows.go`, `Makefile` | `TestSelectDaemonFindsExeBesideClient` finds `agtd.exe`. Windows and darwin test binaries compile. They were not executed on those hosts |
| Host `diff` and `/dev/null` could hide a failed diff as `(no changes)` | `internal/tools/diff.go` | `TestDiffReportsChangesWhenHostDiffIsMissing` clears `PATH`. `TestDiffMatchesGNUDiff` and `TestDiffContextMatchesGNUDiff` match GNU `diff -u` on Linux |
| Docker `--user` used `os.Getuid`/`os.Getgid`, which are `-1` on Windows | `internal/sandbox/user.go`, `docker.go`, `deploy/sandbox.Dockerfile` | `TestDockerUserNeverNegative`. On this Linux host, `TestSandboxRunsTestsAndStaysClosed` wrote a file inside the container and rejected a user containing `-1` |
| `git_push` / `pr_open` were advertised and, after approval, stored as a successful command | `internal/tools/schema.go`, `internal/policy/policy.go`, `internal/orch/orch.go` | `TestRemoteToolsAreNotAdvertised`, `TestScriptedPushIsDeniedNotSuccessful`, `TestApprovalHashIsBoundAndPushStaysUnsupported` |
| `AGT_PROVIDER` was ignored and the sample script could be substituted for any goal | `internal/cli/paths.go`, `cli.go` | `TestProviderFlagRespectsEnvironment`, `TestFixDoesNotSubstituteSampleScript`. `make demo` prints `demo: scripted provider, not a live model` |
| Workspace copy dropped dot-directories and executable bits, and copied dependency trees | `internal/workspace/dir.go`, `internal/tools/filter.go` | `TestMaterializeCopiesDotfilesAndExecutableBits`, `TestMaterializeRejectsOversizedFile`, `TestPatchPreservesExecutableBit` |
| Missing Postgres, Docker, and the sandbox image failed without a next step | `internal/diag/diag.go`, `internal/store/store.go`, `internal/sandbox/preflight.go`, `cmd/agtd/main.go` | `TestRedactDatabaseURLHidesPassword`, `TestPreflightReportsMissingImage`. `agtd` exits when Docker or the image is missing. It does not run tests on the host |

`COMMITTING` closes the sandbox and records success. It does not create a Git commit (`internal/run/state.go`).

Copy rules, any directory depth: include regular files and project dotfiles; refuse symlinks; refuse a file larger than 8 MiB (`MaxCopyFileBytes`). Skip directory names `.git`, `.gradle`, `.mypy_cache`, `.next`, `.pytest_cache`, `.ruff_cache`, `.tox`, `.turbo`, `.venv`, `__pycache__`, `build`, `coverage`, `dist`, `node_modules`, `target`, `vendor`, `venv`.

## Checks run on this machine

Host: Linux amd64. Go 1.23.5. Docker 29.8.1. Postgres 16 via `deploy/compose.yaml` on `127.0.0.1:54329`.

- `make test` passed, including `internal/api`, `internal/orch`, `internal/store`, and `internal/sandbox`.
- `TestSandboxRunsTestsAndStaysClosed` passed in about 5 seconds with `AGT_SANDBOX_IMAGE=agt-sandbox:local`. That run is the container check. A `go test` without that variable skips it.
- `make demo` exited 0. The payrepo retry fixture was repaired inside the sandbox. The source fixture was unchanged. The provider line identified the scripted demo.
- `GOOS=windows` and `GOOS=darwin` `go test -c` succeeded for every package. `go build` succeeded for `windows/amd64`, `darwin/amd64`, `darwin/arm64`, `linux/arm64`, and `linux/amd64`. None of those binaries were executed except the Linux amd64 build used by `make test` and `make demo`.

`internal/testutil.Open` still skips when Postgres is unreachable and `AGT_DATABASE_URL` is unset. That skip is not evidence of database support. `make test` sets the URL and fails if Postgres is down.

## Capability matrix

| Area | State | Evidence |
|---|---|---|
| CLI fix / run / approve / version | Partial | Commands above work. No run list, cancel, pause, resume, diff, export, or apply |
| Daemon and API | Partial | Health, runs, commands, approvals, SSE. Postgres required. No caller authentication. `AGT_LISTEN` is not restricted to loopback yet |
| Run lifecycle | Partial | States and compare-and-swap exist. `COMMITTING` is not a Git commit. Several states have no user workflow |
| Persistence | Partial | Postgres schema `internal/store/migrations/001_init.sql`. No later migration framework. Job leases are still 30 seconds and are not renewed |
| Agent loop | Partial | Fake-provider fixture completes. No live-model repair loop |
| Tools | Partial | Search, read, exact replace, in-process diff, fixed test command. No general create or delete |
| Policy | Partial | Allow, deny, and approval hashing remain. Push and pull request are denied |
| Isolation | Partial on Linux | Per-run container, no network, dropped capabilities, resource limits, host uid on Linux. Windows user id is selected in code and was not run |
| Model adapter | Partial | OpenAI-compatible chat completions. No streaming or retries. API key still required. Only the first tool call is used |
| Accounting | Partial | Token estimate after the model call. Not a hard spend cap |
| Tests | Partial | Unit and Linux Docker/Postgres checks above. One repair fixture |
| Telemetry | Partial | JSON logs and optional OTLP. Not a quality measure |
| Windows native | Unverified | Cross-compile and unit tests of the uid and `.exe` rules. No Windows execution |
| macOS | Unverified | Darwin cross-compile only. Bind-mount uid behavior is not measured |
| Linux | Partial | `make test` and `make demo` passed on this amd64 host. arm64 was cross-compiled only |
| Windows via WSL2 | Unverified | Not run |

## Not in this branch

Milestones 2–8 from the 14 October 2026 audit are not done. In particular: local API authentication, loopback-only listen enforcement, secret redaction, execution profiles for real Python/Go/JavaScript repositories, a live repair loop, patch export and local apply, lease renewal, cancel/pause/resume, installable release artifacts, and an evaluation set of unfamiliar tasks.

The production test command is still `python -m unittest tests.test_retry` in `internal/sandbox/docker.go`. Passing `--repo` does not select another language or test target.

## Handoff

Stay on `milestone-1-correctness` or branch from it. Do not push unless asked.

Milestone 2 next, in this order:

1. Reject non-loopback `AGT_LISTEN` in `internal/config` and `cmd/agtd/main.go`, including IPv6 loopback. Add a test that `0.0.0.0`, a LAN address, and an empty host fail startup.
2. Issue a per-user local token, mode `0600` on Unix, and require it on every route except `GET /healthz`. Send it from `internal/cli`. Test that a caller without the token cannot create a run or approve a command.
3. Reject unexpected browser `Origin` and non-local `Host` values in `internal/api/server.go`.
4. Deny secret filenames by default in `internal/tools` and redact them before `internal/model` sees tool output. Keep path containment. Add cases for `..`, symlinks, absolute paths, and paths with spaces.
5. Assert the sandbox mount set in `internal/sandbox/docker.go` is only the run workspace. Do not mount the home directory, `.ssh`, cloud credentials, or the Docker socket. Keep the network disabled.

After that, Milestone 3 replaces the hardcoded `tests.test_retry` command with explicit Python, Go, and JavaScript profiles and small fixtures whose fixes are not embedded in `eval/scripts/fix_retry.json`.
