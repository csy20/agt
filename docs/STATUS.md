# AGT status

Updated 14 October 2026 on branch `milestone-2-authority`, based on `milestone-1-correctness` at `cb76d5f792bbb2b5ab2da2aa99ab94d40399cb05`.
This file records what the code and the checks actually show. A passing fake-provider demo is not a live-model repair. A cross-compile is not native execution. A skipped integration test is not support.

## Milestone 1

Implemented on `milestone-1-correctness` and still present here. See that commit for the nine correctness fixes: approve flags after ids, absolute client paths, Windows daemon discovery, in-process diffs, a non-negative Docker user, explicit denial of `git_push` and `pr_open`, provider selection, workspace copy rules, and startup diagnostics.

## Milestone 2

Implemented on this branch.

| Control | Where | Check |
|---|---|---|
| `AGT_LISTEN` must be loopback, including `::1` and the literal name `localhost`. Empty host, `0.0.0.0`, and a LAN address fail before Postgres or Docker | `internal/config/listen.go`, `cmd/agtd/main.go` | `TestValidateListenAddr`. `TestNonLoopbackListenExits` runs `agtd`'s startup function for `0.0.0.0:1`, `192.168.1.20:1`, and `:8787` and requires a non-zero exit whose log mentions `AGT_LISTEN` |
| Per-data-dir token at `$AGT_DATA_DIR/auth.token`, mode `0600` on Unix. A group- or world-readable file is replaced. A symlink is refused. `AGT_TOKEN` overrides and is written for the client | `internal/auth` | `TestTokenFileIsPrivateAndStable`, `TestLooseTokenIsRotated`, `TestSymlinkedTokenIsRefused`, `TestEnvironmentTokenIsStoredForTheClient` |
| Every route except `GET /healthz` requires `Authorization: Bearer`. An empty server token fails closed. The 401 body does not echo the token | `internal/api/guard.go`, `internal/cli/cli.go` | `TestMissingTokenCannotCreateOrApprove` posts to create and approve without a token. `TestApproveAcceptsFlagsAfterIDs` checks the bearer header. `make demo` shares `AGT_DATA_DIR` between `agtd` and `agt` |
| Browser `Origin` and a non-loopback `Host` are rejected on every route, including health | `internal/api/guard.go` | `TestBrowserOriginAndRemoteHostAreRejected` |
| Secret filenames and credential directories are not copied, searched, read, or patched. `.env.example`, `.env.sample`, and `.env.template` stay available. `..`, absolute paths, drive and UNC paths, and `:` are rejected. Spaces and Unicode inside the tree are allowed | `internal/tools/secret.go`, `internal/tools/path.go`, `internal/policy/policy.go`, `internal/workspace/dir.go` | `TestSafeRelRejectsEscapes`, `TestSafeRelAllowsSpacesAndUnicode`, `TestReadAllowsContainedNamesAndRefusesSecrets`, `TestSearchSkipsSecretsAndBinary`, `TestSecretPathsAreDenied`, `TestMaterializeSkipsSecretFiles`, `TestSymlinkEscapeIsNotWritten` |
| High-confidence secrets in tool output are redacted before the model sees them and before an event excerpt is stored. The command row keeps the original text | `internal/redact`, `internal/orch/orch.go`, `internal/store/commands.go` | `TestTextHidesSecretsAndKeepsCode`, `TestModelTextRedactsBeforeTheModel` |
| The sandbox argv has one bind mount, the run workspace at `/workspace`. Home, credential directories, and the Docker socket are refused. The container does not inherit the host environment. Network, capability, and resource limits stay in place | `internal/sandbox/mount.go`, `internal/sandbox/env.go`, `internal/sandbox/docker.go` | `TestContainerRunMountsOnlyTheWorktree`, `TestMountSourceRejectsHomeCredentialsAndSocket`, `TestDockerProcessEnvDropsHostSecrets`. `TestSandboxRunsTestsAndStaysClosed` passed in about 2.5 seconds with `AGT_SANDBOX_IMAGE=agt-sandbox:local` and still reported a closed network and an empty host sentinel |

`GET /healthz` stays unauthenticated so the client can see whether `agtd` is up. It still rejects a non-local `Host` and any `Origin`.

Windows ACL code is in `internal/auth/perm_windows.go`. It sets a current-user DACL and replaces a token whose DACL grants anyone else. The Windows junction check is `TestJunctionEscapeIsNotRead`. Both were cross-compiled. Neither was executed on Windows.

A colon in a relative path is rejected on every OS. That blocks Windows drive letters and alternate data streams. A Linux filename that contains `:` cannot be addressed. Backslashes are treated as separators.

## Checks run on this machine

Host: Linux amd64. Go 1.23.5 (`GOTOOLCHAIN=local`). Docker 29.8.1. Postgres 16 via `deploy/compose.yaml` on `127.0.0.1:54329`.

- `make test` passed, including `internal/api`, `internal/auth`, `internal/orch`, `internal/store`, and `internal/sandbox`.
- `TestSandboxRunsTestsAndStaysClosed` passed. That run is the container check. A `go test` without `AGT_SANDBOX_IMAGE` skips it.
- `make demo` exited 0. The client and daemon both used `AGT_DATA_DIR=/tmp/agt-demo`. The token file was mode `600` and the directory was mode `700`. The payrepo retry fixture was repaired inside the sandbox. The provider line identified the scripted demo. Run id `run_7bfea548bc7c41f7`.
- `GOOS=windows GOARCH=amd64`, `GOOS=darwin GOARCH=amd64`, `GOOS=darwin GOARCH=arm64`, and `GOOS=linux GOARCH=arm64` `go test -c` succeeded for the packages touched here, and `go build` succeeded for `cmd/agt` and `cmd/agtd`. None of those binaries were executed. The Linux amd64 binaries used by `make test` and `make demo` were executed.

`internal/testutil.Open` still skips when Postgres is unreachable and `AGT_DATABASE_URL` is unset. That skip is not evidence of database support. `make test` sets the URL and fails if Postgres is down.

## Capability matrix

| Area | State | Evidence |
|---|---|---|
| CLI fix / run / approve / version | Partial | Commands above work and send the local token. No run list, cancel, pause, resume, diff, export, or apply |
| Daemon and API | Partial | Health is open. Other routes require the local token. Listen is loopback-only. Postgres required |
| Run lifecycle | Partial | States and compare-and-swap exist. `COMMITTING` is not a Git commit. Several states have no user workflow |
| Persistence | Partial | Postgres schema `internal/store/migrations/001_init.sql`. No later migration framework. Job leases are still 30 seconds and are not renewed |
| Agent loop | Partial | Fake-provider fixture completes. No live-model repair loop |
| Tools | Partial | Search, read, exact replace, in-process diff, fixed test command. Secret paths are denied. No general create or delete |
| Policy | Partial | Allow, deny, and approval hashing remain. Push and pull request are denied. Secret paths are denied |
| Isolation | Partial on Linux | Per-run container, one workspace mount, no network, dropped capabilities, resource limits, filtered docker CLI environment, host uid on Linux. Windows user id is selected in code and was not run |
| Model adapter | Partial | OpenAI-compatible chat completions. No streaming or retries. API key still required. Only the first tool call is used. Observations are redacted |
| Accounting | Partial | Token estimate after the model call. Not a hard spend cap |
| Tests | Partial | Unit and Linux Docker/Postgres checks above. One repair fixture |
| Telemetry | Partial | JSON logs and optional OTLP. Not a quality measure |
| Windows native | Unverified | Cross-compile, including the ACL and junction tests. No Windows execution |
| macOS | Unverified | Darwin cross-compile only. Bind-mount uid behavior is not measured |
| Linux | Partial | `make test` and `make demo` passed on this amd64 host. arm64 was cross-compiled only |
| Windows via WSL2 | Unverified | Not run |

## Not in this branch

Milestones 3–8 from the 14 October 2026 audit are not done. In particular: execution profiles for real Python/Go/JavaScript repositories, a live repair loop, patch export and local apply, lease renewal, cancel/pause/resume, installable release artifacts, and an evaluation set of unfamiliar tasks.

The production test command is still `python -m unittest tests.test_retry` in `internal/sandbox/docker.go`. Passing `--repo` does not select another language or test target.

## Handoff

Stay on `milestone-2-authority` or branch from it. Do not push unless asked.

Milestone 3 next: replace the hardcoded `python -m unittest tests.test_retry` command with explicit Python, Go, and JavaScript profiles and small fixtures whose fixes are not embedded in `eval/scripts/fix_retry.json`.

The Windows ACL and junction tests still need a Windows run before those controls can be called supported. The roadmap is not complete.
