# agt

`agt` is a local agentic terminal for one workflow: make a failing test pass, show the diff, and stop before any remote side effect. The CLI is a client. `agtd` is the control plane. It accepts only a loopback `AGT_LISTEN`: `127.0.0.1`, another address in `127.0.0.0/8`, `::1`, or the literal name `localhost`. `0.0.0.0`, an empty host, and other interfaces are rejected at startup.

A run is the durable object in Postgres. The model proposes one tool call. Policy allows it, denies it, or asks for an approval bound to that command's argument hash. Tests run in a per-run container with no network. A failed verification returns to the model while step and cost budget remain. The same failure twice, or a repeated edit that already ran, stops the run. The transcript is stored with the run. Repository summaries, plans, and tool output stay in data messages, and the system prompt does not receive them. Edits that change test files require approval. `git push` and opening a pull request do not spawn a process in this build, even after the hash matches.

The fake provider script lives in `eval/scripts/`, outside the fixture repository, so a search of the repo cannot see the answer. It is a labelled demo. `agt` does not load it unless `--script` is passed, and it is not a live model. `--provider` defaults to `AGT_PROVIDER`. There is no silent provider.

`TestScriptedRepairBatch` runs 20 generated fake-provider tasks across Python, Go, and JavaScript. A hidden check is written after the run, outside the worktree. Two tasks edit an in-repo test after approval and fail that hidden check. Two tasks leave the bug and fail verification. The solve rate of that batch is the script's outcome. It is not a hosted-model score.

`make test` starts Postgres, builds the three sandbox images, sets `AGT_INTEGRATION=1`, and fails if Docker, an image, or Postgres is missing. `make unit` does not start those services. On Windows, `scripts/test.ps1` is the development entry. It was not executed on the Linux host that added it. `docs/beta.md` covers setup, upgrade, and uninstall. `scripts/package.sh` writes unsigned archives under `dist/` and does not publish them. The repository has no `LICENSE` file.

## Demo

```sh
make demo
```

With Postgres and `agtd` already running:

```sh
./bin/agt fix --repo eval/fixtures/payrepo --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --provider fake --script eval/scripts/fix_retry.json "make the retry test pass"
```

`--profile` is required. `--repo` does not choose a language or a test command. `agt profile detect --repo PATH` prints a suggestion and does not start a run. The same flags work for the Go fixture (`--profile go --test-arg go --test-arg test --test-arg ./...`, repo `eval/fixtures/gocounter`) and the JavaScript fixture (`--profile javascript --test-arg node --test-arg --test`, repo `eval/fixtures/jsquote`). `typescript` is an alias of the JavaScript profile. These profiles do not install dependencies: the network stays disabled and the image already contains the toolchain.

Reattach after the client exits. Leaving `attach`, including Ctrl+C, does not cancel the run and does not apply the change. `agt run cancel` does. `agt run pause` and `agt run resume` stop and continue a run. `agt run budget-resume` continues a run that stopped for budget, and `--budget-cents` may raise that cap. `agt run cleanup` removes sandbox containers for finished, paused, cancelled, or missing runs. It does not delete the data directory or a container whose run is still executing. `agt run list` prints recent runs. `agt doctor` names the next command when Docker, Postgres, the images, the token, the listen address, or the daemon version is wrong. `--json` prints machine-readable output for `doctor`, `run list`, `run status`, `run attach`, and `fix`. `NO_COLOR` keeps progress lines plain. `agt completion bash|zsh|fish|powershell` prints a completion script.

```sh
./bin/agt doctor
./bin/agt run list
./bin/agt run attach RUN_ID
./bin/agt run pause RUN_ID
./bin/agt run resume RUN_ID
./bin/agt run budget-resume RUN_ID --budget-cents N
./bin/agt run cancel RUN_ID
./bin/agt run cleanup
./bin/agt run diff RUN_ID
./bin/agt run export RUN_ID --output patch.json
./bin/agt run apply RUN_ID --digest DIGEST
```

A cost budget is checked before the next model request, using an estimate of the prepared text plus a reserved output of 2048 tokens, and the reported usage is recorded afterward. That is not a hard dollar cap. Prices of zero, or a provider that does not report usage, do not enforce one. The same limit is not a promise of exactly-once tool execution: `apply_patch` can be applied again safely, and `write_file` and `delete_file` require the previous hash.

`agt run apply` without `--digest` prints the diff, the file operations, and the digest, then exits 2. The daemon rebuilds the patch from the run snapshot and writes only the repository stored for that run. The source tree stays unchanged until that command is authorized. A stale file, or a digest that does not match, writes nothing.

Approve one command, not the conversation. Flags may follow the ids:

```sh
./bin/agt approve RUN_ID COMMAND_ID --args-hash HASH
```

`git push` and opening a pull request are not offered to the model. A request for either is denied. It is not recorded as a successful push or pull request.

The client converts repository and script paths to absolute paths before sending them, so the daemon's working directory does not choose the repository.

The run keeps a filesystem snapshot of the working tree, including uncommitted files, under the data directory. It does not create a Git worktree, and `COMMITTING` does not create a Git commit. The machine-applicable record is a `patch.v1` artifact with exact bytes and modes. The diff printed in the terminal is a preview and can be shorter than that artifact. The copied workspace keeps executable bits and project files such as `.github` and `.gitignore`. It skips `.git`, `node_modules`, `vendor`, and common build directories (`build`, `dist`, `target`, and similar). A repository nested inside the data directory, or a data directory nested inside the repository, is rejected. It also skips secret names such as `.env`, private keys, and the directories `.ssh`, `.aws`, `.gnupg`, `.kube`, and `.docker`. `.env.example`, `.env.sample`, and `.env.template` are copied. A file over 8 MiB fails the copy. Symlinks are refused. Paths that leave the worktree, including `..`, absolute paths, drive letters, and UNC paths, are rejected. A path containing `:` is rejected so a Windows alternate data stream cannot be addressed.

Unix hosts pass the host uid and gid to Docker. Native Windows uses the unprivileged image user `65532:65532`. The daemon does not pass `-1:-1`, and it does not run tests on the host when Docker is missing. The test container mounts only that run's workspace at `/workspace`. It does not mount the home directory, SSH or cloud credential directories, or the Docker socket, and it does not receive the host environment. The client sends a profile name and an argv. The daemon chooses the image. A request that includes an image is rejected. Python, Go, and JavaScript use `agt-sandbox:python`, `agt-sandbox:go`, and `agt-sandbox:node` unless `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, or `AGT_IMAGE_NODE` is set. `AGT_SANDBOX_IMAGE` is the Python image. Startup fails if a configured image is missing. `/tmp` in the container is executable because `go test` writes and runs its test binary there. The root filesystem stays read-only, and `/tmp` stays `nosuid`.

`GET /healthz` and `GET /v1/identity` do not require a token. Every other route requires `Authorization: Bearer` with the token in `$AGT_DATA_DIR/auth.token`. On Unix the file is mode `0600`. A group- or world-readable token file is replaced. The client reads `AGT_TOKEN` when it is set, and otherwise reads that file, so the client and daemon need the same `AGT_DATA_DIR` unless `AGT_TOKEN` is set for both. The client accepts a daemon only when `/healthz` sends `X-Agt-Product: agt` and `X-Agt-Version` equal to this `agt`. An HTTP 200 from another program is not enough. Requests with a browser `Origin` or a non-loopback `Host` are rejected. Tool output is redacted before it is given to the model.

On Linux the data directory stays `~/.local/share/agt` when `AGT_DATA_DIR` is unset, even if `XDG_DATA_HOME` is set. Config is `$XDG_CONFIG_HOME/agt` or `~/.config/agt`. Cache is `$XDG_CACHE_HOME/agt` or `~/.cache/agt`. A Darwin build uses `~/Library/Application Support/agt` and `~/Library/Caches/agt`. A Windows build uses `%AppData%\agt` and `%LocalAppData%\agt`. Those Darwin and Windows paths were not executed on this machine. `AGT_CONFIG_DIR` and `AGT_CACHE_DIR` override the config and cache directories. The daemon log is `$AGT_DATA_DIR/agtd.log`. `agtd` writes `agtd.pid` and `agtd.addr` while it is listening and removes them on a clean shutdown. A daemon started by `agt` keeps running after the client exits. `--model` checks the daemon's `AGT_MODEL_NAME` and does not call a hosted model. Attach reconnects from the last event it printed.

## Interactive session

On a terminal, `agt` with no arguments opens a session in the current repository. A pipe still prints usage. `agt tui --dump` prints example frames and does not start a run or change repository files. `agt tui --demo` is that same example inside the terminal.

```sh
agt tui --dump
agt setup --repo . --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --confirm
```

The session shows assistant text as it arrives, collapsible tool lines, and an approval bound to the command's argument hash. `ctrl+y` and `ctrl+n` post that full hash. `y` and `n` do that only while the confirm line is focused. Text in the composer never approves or applies. The routine view abbreviates the hash and the digest. `ctrl+f` shows the full values, and the apply confirmation shows the full digest. `/diff` shows the preview. `/apply` posts that digest. Ctrl+C leaves the screen and restores the terminal. It does not cancel the run and does not apply a patch. A pipe refuses the session.

`/plan` and `/ask` send interaction `plan` or `ask`. The daemon denies writes, test runs, and other mutations in those modes, including a create that does not come from this screen. They finish with the answer and do not require a failing baseline. The repair state `PLANNING` still executes. `/model` shows the daemon's `AGT_MODEL_NAME` and does not switch models. The status line labels the cent limit as an estimate. `@path` names a repository file. The file bytes are not copied into the goal. A follow-up `/fix` starts a new run from a fresh snapshot. An unapplied patch stays visible and unapplied until its digest is confirmed. The session says that patch's edits are absent from the new snapshot. Example scenes use the payrepo fixture and are not available in a live session.

A session is stored apart from its runs and is bound to one canonical repository path. `agt session new` creates that record and does not start a run. `agt session list` lists sessions for the path. `agt session open` attaches when a run is still active and prints the ordered history when the session is idle. `agt session show` prints that history and does not attach. Open and show do not start work, replay a tool, or apply a patch. `agt session submit` stores one goal. Omit `--message-id` and the client prints a generated id before the request. Repeating that id returns the original run. A new prompt while a run is active is refused and does not change that run's goal. A session bound to one repository is not silently pointed at another. `/sessions` opens the same list inside the screen. Choosing a session reopens it. Choosing a new session clears the selection and does not create a run until a prompt is sent. The next run can carry bounded history and prior results as `DATA origin=session-history`. Facts that do not fit become `DATA origin=session-summary`. Repository content, summaries, and tool output stay in those tagged messages.

```sh
agt session new --repo .
agt session list --repo .
agt session show SESSION
agt session open SESSION
```

`agt setup --confirm` saves the profile and the test command. It does not choose an image, start `agtd`, or start a run. Empty test argv is an error. Bubble Tea, Bubbles, and Lip Gloss draw the screen. `agt fix` and `--json` stay available for scripts.

## Layout

| Path | Role |
|---|---|
| `cmd/agt` | CLI |
| `cmd/agtd` | Control plane and worker |
| `internal/tui` | Interactive session |
| `internal/client` | Daemon client used by the session |
| `internal/setup` | Confirmed profile and test command |
| `internal/policy` | Allow, deny, or require approval |
| `internal/sandbox` | Per-run Docker test execution |
| `internal/profile` | Python, Go, and JavaScript execution profiles |
| `eval/fixtures/payrepo` | Buggy Python sample |
| `eval/fixtures/gocounter` | Buggy Go sample |
| `eval/fixtures/jsquote` | Buggy JavaScript sample |
| `eval/scripts/` | Scripted model steps, kept outside the fixtures |
| `docs/beta.md` | Fresh-machine setup, upgrade, and uninstall |
| `scripts/test.ps1` | PowerShell unit and integration entry. Not executed on the packaging host |
| `scripts/package.sh` | Local unsigned `agt` and `agtd` archives under `dist/` |

`agtd` reads `AGT_DATABASE_URL`, `AGT_SANDBOX_IMAGE`, `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, `AGT_IMAGE_NODE`, `AGT_LISTEN`, `AGT_DATA_DIR`, `AGT_CONFIG_DIR`, `AGT_CACHE_DIR`, and `AGT_PROVIDER`. Set `AGT_TOKEN` to choose the local bearer token; otherwise `agtd` creates `$AGT_DATA_DIR/auth.token`. An OpenAI-compatible provider is selected with `--provider openai`, `AGT_MODEL_BASE_URL`, and `AGT_MODEL_NAME`. `AGT_MODEL_API_KEY` is required unless the base URL is loopback (`localhost`, `::1`, or a loopback address). The default base URL is `https://api.openai.com/v1`. Set `AGT_OTLP_ENDPOINT` to export traces; export failure does not fail the run. `agt doctor` is the setup check. It does not start the daemon.

## Not in this build

Kubernetes, a durable workflow engine, a separate vector database, multi-agent workers, a web console, Stripe, and automatic long-term memory.
