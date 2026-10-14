# AGT status

Updated 14 October 2026 on branch `milestone-3-profiles`, based on `milestone-2-authority` at `4a4924aaae5a974ea63297645bbd7f11f5291567`.
This file records what the code and the checks actually show. A passing fake-provider demo is not a live-model repair. A cross-compile is not native execution. A skipped integration test is not support.

## Milestone 1

Implemented on `milestone-1-correctness` at `cb76d5f792bbb2b5ab2da2aa99ab94d40399cb05` and still present here. See that commit for the nine correctness fixes: approve flags after ids, absolute client paths, Windows daemon discovery, in-process diffs, a non-negative Docker user, explicit denial of `git_push` and `pr_open`, provider selection, workspace copy rules, and startup diagnostics.

## Milestone 2

Implemented on `milestone-2-authority` and still present here.

| Control | Where | Check |
|---|---|---|
| `AGT_LISTEN` must be loopback, including `::1` and the literal name `localhost`. Empty host, `0.0.0.0`, and a LAN address fail before Postgres or Docker | `internal/config/listen.go`, `cmd/agtd/main.go` | `TestValidateListenAddr`. `TestNonLoopbackListenExits` runs `agtd`'s startup function for `0.0.0.0:1`, `192.168.1.20:1`, and `:8787` and requires a non-zero exit whose log mentions `AGT_LISTEN` |
| Per-data-dir token at `$AGT_DATA_DIR/auth.token`, mode `0600` on Unix. A group- or world-readable file is replaced. A symlink is refused. `AGT_TOKEN` overrides and is written for the client | `internal/auth` | `TestTokenFileIsPrivateAndStable`, `TestLooseTokenIsRotated`, `TestSymlinkedTokenIsRefused`, `TestEnvironmentTokenIsStoredForTheClient` |
| Every route except `GET /healthz` requires `Authorization: Bearer`. An empty server token fails closed. The 401 body does not echo the token | `internal/api/guard.go`, `internal/cli/cli.go` | `TestMissingTokenCannotCreateOrApprove` posts to create and approve without a token. `TestApproveAcceptsFlagsAfterIDs` checks the bearer header. `make demo` shares `AGT_DATA_DIR` between `agtd` and `agt` |
| Browser `Origin` and a non-loopback `Host` are rejected on every route, including health | `internal/api/guard.go` | `TestBrowserOriginAndRemoteHostAreRejected` |
| Secret filenames and credential directories are not copied, searched, read, written, deleted, or patched. `.env.example`, `.env.sample`, and `.env.template` stay available. `..`, absolute paths, drive and UNC paths, and `:` are rejected. Spaces and Unicode inside the tree are allowed | `internal/tools/secret.go`, `internal/tools/path.go`, `internal/policy/policy.go`, `internal/workspace/dir.go` | `TestSafeRelRejectsEscapes`, `TestSafeRelAllowsSpacesAndUnicode`, `TestReadAllowsContainedNamesAndRefusesSecrets`, `TestSearchSkipsSecretsAndBinary`, `TestSecretPathsAreDenied`, `TestWriteAndDeleteStayInsideTheTree`, `TestMaterializeSkipsSecretFiles`, `TestSymlinkEscapeIsNotWritten` |
| High-confidence secrets in tool output are redacted before the model sees them and before an event excerpt is stored. The command row keeps the original text | `internal/redact`, `internal/orch/orch.go`, `internal/store/commands.go` | `TestTextHidesSecretsAndKeepsCode`, `TestModelTextRedactsBeforeTheModel` |
| The sandbox argv has one bind mount, the run workspace at `/workspace`. Home, credential directories, and the Docker socket are refused. The container does not inherit the host environment. Network, capability, and resource limits stay in place | `internal/sandbox/mount.go`, `internal/sandbox/env.go`, `internal/sandbox/docker.go` | `TestContainerRunMountsOnlyTheWorktree`, `TestMountSourceRejectsHomeCredentialsAndSocket`, `TestDockerProcessEnvDropsHostSecrets`, `TestSandboxRunsTestsAndStaysClosed` |

`GET /healthz` stays unauthenticated so the client can see whether `agtd` is up. It still rejects a non-local `Host` and any `Origin`.

Windows ACL code is in `internal/auth/perm_windows.go`. It sets a current-user DACL and replaces a token whose DACL grants anyone else. The Windows junction check is `TestJunctionEscapeIsNotRead`. Both were cross-compiled in Milestone 2. Neither was executed on Windows. This milestone did not recompile `internal/auth`. It did cross-compile `internal/tools`, which contains the junction test, and that binary was not executed.

A colon in a relative path is rejected on every OS. That blocks Windows drive letters and alternate data streams. A Linux filename that contains `:` cannot be addressed. Backslashes are treated as separators.

## Milestone 3

Implemented on this branch. The production test command is the profile argv. `internal/sandbox/docker.go` does not default to `python -m unittest tests.test_retry`. Passing `--repo` does not select a language or a test target. `agt fix` and `agt run create` exit 2 without `--profile` and do not POST. `agt profile detect` prints a suggestion and exits 0 without creating a run.

| Control | Where | Check |
|---|---|---|
| Contract `profile.v1`. Names `python`, `go`, and `javascript`. `typescript` is an alias of `javascript`. The client sends the name and argv. The daemon fills image, `linux/` plus `runtime.GOARCH`, the env allowlist, network `none`, and deps `prebuilt`. An `image` field is rejected. `--prepare` is rejected. Allowed argv[0] values are `python`, `python3`, `go`, and `node` | `internal/profile`, `internal/api/server.go`, `internal/cli/profile.go` | `TestResolveFillsDaemonFields`, `TestTypeScriptIsTheJavaScriptProfile`, `TestRequestRejectsShellPrepareAndLimits`, `TestHostEnvDropsSecrets`, `TestPrepareAndShellArgvAreRejectedLocally`, `TestHealthIdempotencyAndOpenAIGuard` (missing profile is 400; `profile.image` is 400; a `go.mod` beside an explicit Python profile stays Python) |
| Discovery may suggest one language from `go.mod`, `package.json`, or Python markers. More than one category suggests nothing. Secret directories and symlinks are not followed. A suggestion does not start a run | `internal/profile/detect.go` | `TestProposeDoesNotAuthorize`, `TestFixWithoutProfileDoesNotStartARun`, `TestProfileDetectPrintsASuggestion` |
| The run stores canonical `profile_json`. The request hash includes it. An empty object fails closed at test execution. The run view exposes profile identity and still omits `script_json` | `internal/store/migrations/002_profile.sql`, `internal/store/store.go`, `internal/api/server.go` | Store migration and idempotency tests in `make test`. The Go/JavaScript repair test reads the stored name and argv |
| `write_file` and `delete_file` require `before_sha256`, stay inside the worktree, refuse secrets and symlinks, and are idempotent when the content hash already matches. `apply_patch` remains. Conflict returns exit 2 and does not write | `internal/tools/edit.go`, `internal/policy/policy.go`, `internal/orch/orch.go` | `TestWriteFileIsHashedAtomicAndIdempotent`, `TestDeleteFileIsHashedAndIdempotent`, `TestWriteAndDeleteShowInTheDiff`, `TestWriteAndDeleteStayInsideTheTree` |
| Three pinned images, tags not digests: `deploy/python.Dockerfile` (`python:3.12-alpine`), `deploy/go.Dockerfile` (`golang:1.23-alpine`), `deploy/node.Dockerfile` (`node:22-alpine`), each with user `65532`. Tags `agt-sandbox:python`, `agt-sandbox:go`, `agt-sandbox:node`. Startup preflight checks every configured image and is fatal. At exec the stored image must still equal the configured image for that profile. `/tmp` is `rw,exec,nosuid,size=128m` because Docker's default tmpfs is `noexec` and `go test` runs its binary there. The root stays read-only. Network stays `none` | `deploy/*.Dockerfile`, `internal/sandbox` | `TestPreflightReportsMissingImage`, `TestContainerRunMountsOnlyTheWorktree` asserts the `exec` tmpfs option, `TestSandboxRunsTestsAndStaysClosed` |
| Small fixtures whose fixes live outside the copied tree: `eval/fixtures/payrepo` with `eval/scripts/fix_retry.json`, `eval/fixtures/gocounter` with `eval/scripts/fix_gocounter.json`, `eval/fixtures/jsquote` with `eval/scripts/fix_jsquote.json`. The Go repair uses `write_file`, memory `512m`, and 128 PIDs. The source fixtures stay unmodified | `eval/fixtures`, `eval/scripts`, `internal/orch/orch_test.go` | `TestHappyPathFixesRetry`, `TestGoAndJavaScriptProfilesRepairDistinctFailures` |

The Go profile env sets `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`, and `CGO_ENABLED=0`. The Go fixture has no external module requirements. The container network is not opened for these profiles.

`internal/testutil` runs the profile on the host only when none of `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, and `AGT_IMAGE_NODE` is set. If any one is set, all three must be set and must exist. That host path is not container support. Production `cmd/agtd` uses `sandbox.Runner` only.

## Checks run on this machine

Host: Linux amd64. Go 1.23.5 (`GOTOOLCHAIN=local`). Docker 29.8.1. Postgres 16 via `deploy/compose.yaml` on `127.0.0.1:54329`.

- `make test` passed. It builds `agt-sandbox:python`, `agt-sandbox:go`, and `agt-sandbox:node`, exports those tags, and runs `go test ./...`. `internal/api` passed in about 9 seconds, `internal/orch` in about 55 seconds, and `internal/sandbox` in about 4 seconds. `TestSandboxRunsTestsAndStaysClosed` ran against the Python image. `TestGoAndJavaScriptProfilesRepairDistinctFailures` ran `go test ./...` in `agt-sandbox:go` and `node --test` in `agt-sandbox:node`. The first `go test` failed, the scripted `write_file` applied, and the verifier passed. The source fixtures were unchanged.
- `make demo` exited 0. The client and daemon both used `AGT_DATA_DIR=/tmp/agt-demo`. The token file was mode `600` and the directory was mode `700`. The command passed `--profile python` and the four unittest argv entries. The payrepo retry fixture was repaired inside the sandbox. The provider line identified the scripted demo. Run id `run_0c3fc8d8e0165c95`. The demo output did not print the token.
- `GOOS=windows GOARCH=amd64`, `GOOS=darwin GOARCH=amd64`, `GOOS=darwin GOARCH=arm64`, and `GOOS=linux GOARCH=arm64` `go test -c` succeeded for `cmd/agtd`, `internal/api`, `internal/cli`, `internal/config`, `internal/orch`, `internal/policy`, `internal/profile`, `internal/sandbox`, `internal/store`, `internal/tools`, and `internal/workspace`. `go build` succeeded for `cmd/agt` and `cmd/agtd` on those four targets. None of those binaries were executed. The scratch binaries were deleted. The Linux amd64 binaries used by `make test` and `make demo` were executed.

`internal/testutil.Open` still skips when Postgres is unreachable and `AGT_DATABASE_URL` is unset. That skip is not evidence of database support. `make test` sets the URL and fails if Postgres is down. A `go test` that does not export the three image variables may use the host runner. Do not record that run as container support.

## Capability matrix

| Area | State | Evidence |
|---|---|---|
| CLI fix / run / approve / version | Partial | Commands above work and send the local token. `fix` and `run create` require `--profile`. `profile detect` only suggests. No run list, cancel, pause, resume, diff export, or local apply |
| Daemon and API | Partial | Health is open. Other routes require the local token. Listen is loopback-only. Postgres required. Create rejects a missing profile and a client-supplied image |
| Run lifecycle | Partial | States and compare-and-swap exist. `COMMITTING` is not a Git commit. Several states have no user workflow |
| Persistence | Partial | `001_init.sql` plus `002_profile.sql` (`profile_json`). Job leases are still 30 seconds and are not renewed |
| Agent loop | Partial | Fake-provider fixtures complete for one Python, one Go, and one JavaScript failure. No live-model repair loop |
| Tools | Partial | Search, read, exact replace, hash-preconditioned write and delete, in-process diff. The test argv comes from the profile. Secret paths are denied |
| Policy | Partial | Allow, deny, and approval hashing remain. Push and pull request are denied. Secret paths and escapes are denied for write and delete |
| Isolation | Partial on Linux | Three per-language images, one workspace mount, no network, dropped capabilities, resource limits from the profile, filtered docker CLI environment, host uid on Linux. `/tmp` is executable and `nosuid`. Windows user id is selected in code and was not run |
| Model adapter | Partial | OpenAI-compatible chat completions. No streaming or retries. API key still required. Only the first tool call is used. Observations are redacted |
| Accounting | Partial | Token estimate after the model call. Not a hard spend cap |
| Tests | Partial | Unit and Linux Docker/Postgres checks above. Three scripted repair fixtures |
| Telemetry | Partial | JSON logs and optional OTLP. Not a quality measure |
| Windows native | Unverified | Cross-compile, including the junction test in `internal/tools`. ACL code was cross-compiled in Milestone 2 and was not executed. No Windows execution |
| macOS | Unverified | Darwin cross-compile only. Bind-mount uid behavior is not measured |
| Linux | Partial | `make test` and `make demo` passed on this amd64 host. arm64 was cross-compiled only |
| Windows via WSL2 | Unverified | Not run |

## Not in this branch

Milestones 4–8 from the 14 October 2026 audit are not done. In particular: a live repair loop, patch export and local apply, lease renewal, cancel/pause/resume, installable release artifacts, and an evaluation set of unfamiliar tasks. The three fixtures use the fake provider. They are not evidence that a live model repaired a repository.

The roadmap is not complete.

## Handoff

Stay on `milestone-3-profiles` or branch from it. Do not push unless asked.

Milestone 4 next: a real model repair loop. The profile, the test argv, and the approval boundary stay as they are here. Do not treat the fake-provider fixtures as that loop.

The Windows ACL and junction tests still need a Windows run before those controls can be called supported. macOS and linux/arm64 were not executed.
