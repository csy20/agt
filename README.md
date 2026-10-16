# agt

`agt` is a local agentic terminal. A repair makes a failing test pass. A feature or a refactor runs checks from a verification contract supplied before work starts. Each path shows the diff and stops before any remote side effect. The CLI is a client. `agtd` is the control plane. It accepts only a loopback `AGT_LISTEN`: `127.0.0.1`, another address in `127.0.0.0/8`, `::1`, or the literal name `localhost`. `0.0.0.0`, an empty host, and other interfaces are rejected at startup.

A run is the durable object in Postgres. The model proposes one tool call. Policy allows it, denies it, or asks for an approval bound to that command's argument hash. Tests run in a per-run container with no network. A failed verification returns to the model while step and cost budget remain. The same failure twice, or a repeated edit that already ran, stops the run. The transcript is stored with the run. Repository summaries, plans, and tool output stay in data messages, and the system prompt does not receive them. Edits that change test files require approval. `git push` and opening a pull request do not spawn a process in this build, even after the hash matches.

The fake provider script lives in `eval/scripts/`, outside the fixture repository, so a search of the repo cannot see the answer. It is a labelled demo. `agt` does not load it unless `--script` is passed, and it is not a live model. `--provider` defaults to `AGT_PROVIDER`. There is no silent provider.

`TestScriptedRepairBatch` runs 20 generated fake-provider tasks across Python, Go, and JavaScript. A hidden check is written after the run, outside the worktree. Two tasks edit an in-repo test after approval and fail that hidden check. Two tasks leave the bug and fail verification. The solve rate of that batch is the script's outcome. It is not a hosted-model score.

`make test` starts Postgres, builds the three sandbox images, sets `AGT_INTEGRATION=1`, and fails if Docker, an image, or Postgres is missing. It connects to the `postgres` maintenance database, creates disposable `agt_test_*` databases, and does not reset or drop database `agt`. `make unit` does not start those services. On Windows, `scripts/test.ps1` is the development entry. It was not executed on the Linux host that added it. `docs/beta.md` covers setup, upgrade, and uninstall. `scripts/package.sh` writes unsigned archives under `dist/` and does not publish them. The repository has no `LICENSE` file.

## Demo

```sh
make demo
```

With Postgres and `agtd` already running:

```sh
./bin/agt fix --repo eval/fixtures/payrepo --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --provider fake --script eval/scripts/fix_retry.json "make the retry test pass"
```

`--profile` is required. `--repo` does not choose a language or a test command. `agt profile detect --repo PATH` prints a suggestion and does not start a run. The same flags work for the Go fixture (`--profile go --test-arg go --test-arg test --test-arg ./...`, repo `eval/fixtures/gocounter`) and the JavaScript fixture (`--profile javascript --test-arg node --test-arg --test`, repo `eval/fixtures/jsquote`). `typescript` is an alias of the JavaScript profile. These profiles do not install dependencies: the network stays disabled and the image already contains the toolchain.

`repair`, `feature`, and `refactor` are task types. `agent`, `ask`, and `plan` stay interaction modes. `risk_level` is neither. `agt fix` stays a repair and rejects a contract. Ask and plan stay read-only, finish with outcome `answered`, and do not run baseline or repair verification.

`agt feature` and `agt refactor` require a `verify.v1` contract. `--contract-file` is read by this client. The API receives the object and does not open a path from the request. Empty checks, an escaping path, an argv that does not match the named profile, and a contract on a repair, ask, or plan request are rejected before work starts. The daemon stores the validated contract and its digest. Model output and edits in the run copy cannot replace it. Changing those checks is a new run and a fresh baseline. The profile and its test argv stay explicit. The daemon selects the image and runs each contract check through that profile runner. There is no shell and no automatic dependency installation.

A feature requires a passing regression baseline, a real acceptance failure before editing, a passing acceptance check afterwards, and regression checks that still pass. Outcome `feature_implemented` is that verifier result. Acceptance that already passes is `already_satisfied`: the requested behavior is already satisfied, and the run does not claim a newly implemented feature. A missing executable, a timeout, an empty test run, or an import error is `verification_infrastructure`, not the intended acceptance failure. A refactor requires the same protected checks to pass before and after the edit. Outcome `checks_passed` means those checks passed. It is not a proof of behavioral equivalence. A failing refactor baseline is `refactor_baseline_failed` and is not a verified refactor. The model's statement that tests passed is not the verdict.

Migration `008_verify.sql` stores the task type, contract, contract digest, baseline results, final results, verification outcome, and the patch artifact digest. Older runs stay `repair` with an empty contract. Apply still requires `SUCCEEDED`, the artifact digest, and unchanged source files. A stored verification digest that does not match that artifact writes nothing. Approval of a test edit still requires the argument hash and does not revise the contract. Paths named by the contract cannot be edited in that run.

```sh
./bin/agt feature --repo eval/fixtures/featuregate --profile python --test-arg python3 --test-arg -m --test-arg unittest --test-arg tests.test_regress --contract-file feature.json --provider fake --script FILE "make greet return hello"
```

`eval/fixtures/featuregate/greet.py` returns `"hi"`. `tests/test_accept.py` expects `"hello"`. `tests/test_regress.py` expects a non-empty string. One contract for that tree is:

```json
{
  "version": "verify.v1",
  "checks": [
    {"name": "reg", "role": "regression", "argv": ["python3", "-m", "unittest", "tests.test_regress"], "paths": ["tests/test_regress.py"]},
    {"name": "accept", "role": "acceptance", "argv": ["python3", "-m", "unittest", "tests.test_accept"], "paths": ["tests/test_accept.py"]}
  ]
}
```

`eval/fixtures/refactorgate/greet.py` already returns `"hello"` through a local name. `tests/test_protected.py` expects `"hello"`. Its contract uses role `protected` and the same `python3 -m unittest tests.test_protected` argv. `agt refactor` takes that file with `--contract-file`. Scripted provider answers stay outside these trees. The verifier executes the checks. `eval/fixtures/payrepo` is unchanged.

In the session, `/feature FILE goal` and `/refactor FILE goal` use the same submission path. The first token is the contract file, read by the client. Composer text does not approve or apply. The screen shows the task type, the contract checks, the verification outcome, and failures.

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

`GET /healthz` and `GET /v1/identity` do not require a token. Every other route requires `Authorization: Bearer` with the token in `$AGT_DATA_DIR/auth.token`. On Unix the file is mode `0600`. A group- or world-readable token file is replaced. The client reads `AGT_TOKEN` when it is set, and otherwise reads that file, so the client and daemon need the same `AGT_DATA_DIR` unless `AGT_TOKEN` is set for both. The client accepts a daemon only when `/healthz` sends `X-Agt-Product: agt`, `X-Agt-Version` equal to this `agt`, `X-Agt-Contract: api.v2`, and `X-Agt-Capabilities` containing `sessions` and `models`. `X-Agt-Build` is the local build id. A daemon whose product version matches, but which omits that contract or those capabilities, is rejected. The message says to rebuild `agtd` from this checkout with `make build` and to restart that daemon yourself. This command does not stop another process. An HTTP 200 from another program is not enough. Requests with a browser `Origin` or a non-loopback `Host` are rejected. Tool output is redacted before it is given to the model.

On Linux the data directory stays `~/.local/share/agt` when `AGT_DATA_DIR` is unset, even if `XDG_DATA_HOME` is set. Config is `$XDG_CONFIG_HOME/agt` or `~/.config/agt`. Cache is `$XDG_CACHE_HOME/agt` or `~/.cache/agt`. A Darwin build uses `~/Library/Application Support/agt` and `~/Library/Caches/agt`. A Windows build uses `%AppData%\agt` and `%LocalAppData%\agt`. Those Darwin and Windows paths were not executed on this machine. `AGT_CONFIG_DIR` and `AGT_CACHE_DIR` override the config and cache directories. The daemon log is `$AGT_DATA_DIR/agtd.log`. `agtd` writes `agtd.pid` and `agtd.addr` while it is listening and removes them on a clean shutdown. A daemon started by `agt` keeps running after the client exits. `--model` checks the daemon's `AGT_MODEL_NAME` and does not call a hosted model. Attach reconnects from the last event it printed.

## Interactive session

On a terminal, `agt` with no arguments opens a session in the current repository. A pipe still prints usage. `agt tui --dump` prints example frames and does not start a run or change repository files. `agt tui --demo` is that same example inside the terminal.

```sh
agt tui --dump
agt setup --repo . --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --confirm
```

The session shows assistant text as it arrives, collapsible tool lines, and an approval bound to the command's argument hash. `ctrl+y` and `ctrl+n` post that full hash. `y` and `n` do that only while the confirm line is focused. Text in the composer never approves or applies. The routine view abbreviates the hash and the digest. `ctrl+f` shows the full values, and the apply confirmation shows the full digest. `/diff` shows the preview. `/apply` posts that digest. Ctrl+C leaves the screen and restores the terminal. It does not cancel the run and does not apply a patch. A pipe refuses the session.

`/plan` and `/ask` send interaction `plan` or `ask`. The daemon denies writes, test runs, and other mutations in those modes, including a create that does not come from this screen. They finish with the answer and do not require a failing baseline. The repair state `PLANNING` still executes. `/feature FILE goal` and `/refactor FILE goal` stay interaction `agent` and send the contract object with the goal. `/model` lists saved configurations. With a session open, the choice is stored on that session. With no session, it is stored as the daemon default in `$AGT_DATA_DIR/model-default`. Neither choice calls a model. The status line shows `config · model`, prefixes a fake provider with `scripted`, and labels the cent limit as an estimate. `@path` names a repository file. The file bytes are not copied into the goal. A follow-up `/fix` starts a new run from a fresh snapshot. An unapplied patch stays visible and unapplied until its digest is confirmed. The session says that patch's edits are absent from the new snapshot. Example scenes use the payrepo fixture and are not available in a live session.

A session is stored apart from its runs and is bound to one canonical repository path. `agt session new` creates that record and does not start a run. `agt session list` lists sessions for the path. `agt session open` attaches when a run is still active and prints the ordered history when the session is idle. `agt session show` prints that history and does not attach. Open and show do not start work, replay a tool, or apply a patch. `agt session submit` stores one goal. Omit `--message-id` and the client prints a generated id before the request. Repeating that id returns the original run. A new prompt while a run is active is refused and does not change that run's goal. A session bound to one repository is not silently pointed at another. `/sessions` opens the same list inside the screen. Choosing a session reopens it. Choosing a new session clears the selection and does not create a run until a prompt is sent. The next run can carry bounded history and prior results as `DATA origin=session-history`. Facts that do not fit become `DATA origin=session-summary`. Repository content, summaries, and tool output stay in those tagged messages.

```sh
agt session new --repo .
agt session list --repo .
agt session show SESSION
agt session open SESSION
agt model list
agt model select NAME
agt model select NAME --session SESSION --repo .
agt agent list
```

`agt agent list` separates model API configurations from Codex and Claude. A model endpoint is not a coding agent. The command does not call a model and does not start either CLI. `agt agent run` is refused. Tests can parse scripted `codex exec --json` and Claude `stream-json` fixtures. Those fixtures are not a live agent. `agt agent ask --authorize-live --repo PATH --prompt TEXT` can start one prompt-only Codex app-server turn. It does not start without that flag. A saved ChatGPT login is not permission, and this path does not use an API key. The turn uses experimental permission profile `agt-snapshot`, disables `shell_tool`, and sends tagged excerpts only. Before `turn/start`, `command/exec` under that profile must read the snapshot and must fail sibling, traversal, symlink, home, write, and network probes. Legacy `readOnly` has no `readableRoots` and is not the boundary. `readOnly.access` is rejected by codex-cli 0.162.1. When the probe fails, the prompt is not sent. A stub, or a runtime whose own version is not in the app-server user agent, is labelled `scripted`. An executable named `codex` is not enough to mark a turn live. Claude is not launched. The request is limited to five minutes. The completed turn's limitation is `prompt-only; agent tools disabled; tagged excerpts only`. Those excerpts are `payments/processor.py` and `tests/test_retry.py` when both are regular files in the snapshot. A repository without those files gets the prompt and that limitation, and no file body. Scripted fixture parses, stub completions, and a blocked preflight are not that turn. One such turn completed from source revision `54d1e1c86f8cebfda867133ff4821b41fcb3a3d9` with build `local+54d1e1c86f8c-dirty.20261016T160207Z`. The tree that contains this note is dirty and unpublished. Binary digests, the check commands, and the log location are in `docs/STATUS.md`.

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
| `eval/fixtures/featuregate` | Feature contract sample. Acceptance expects `hello`; `greet` returns `hi` |
| `eval/fixtures/refactorgate` | Refactor contract sample. The protected check already expects `hello` |
| `internal/verify` | `verify.v1` contract validation and verifier outcomes |
| `eval/fixtures/gocounter` | Buggy Go sample |
| `eval/fixtures/jsquote` | Buggy JavaScript sample |
| `eval/scripts/` | Scripted model steps, kept outside the fixtures |
| `docs/beta.md` | Fresh-machine setup, upgrade, and uninstall |
| `scripts/test.ps1` | PowerShell unit and integration entry. Not executed on the packaging host |
| `scripts/package.sh` | Local unsigned `agt` and `agtd` archives under `dist/` |

`agtd` reads `AGT_DATABASE_URL`, `AGT_SANDBOX_IMAGE`, `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, `AGT_IMAGE_NODE`, `AGT_LISTEN`, `AGT_DATA_DIR`, `AGT_CONFIG_DIR`, `AGT_CACHE_DIR`, and `AGT_PROVIDER`. Set `AGT_TOKEN` to choose the local bearer token; otherwise `agtd` creates `$AGT_DATA_DIR/auth.token`. The configuration named `default` still comes from `AGT_PROVIDER`, `AGT_MODEL_NAME`, `AGT_MODEL_BASE_URL`, `AGT_MODEL_API_KEY`, `AGT_INPUT_USD_PER_M`, and `AGT_OUTPUT_USD_PER_M`. For that one OpenAI configuration, an unset base URL remains `https://api.openai.com/v1`, and the key is required unless the base URL is loopback (`localhost`, `::1`, or a loopback address). `AGT_MODELS` adds more names. Each name uses `AGT_MODEL_<NAME>_ADAPTER`, `ENDPOINT`, `MODEL`, `INPUT_USD_PER_M`, `OUTPUT_USD_PER_M`, `API_KEY_ENV`, and `SCRIPT`. A named OpenAI configuration must set its own endpoint. An empty endpoint is invalid and is not filled from the hosted default. A non-loopback endpoint requires the named variable to be set. The variable's value is not returned by the API or written into the run. `agt model list` and `agt model select` do not call a model. `--provider` on one command still selects that run's adapter. A failed call does not try another endpoint. Set `AGT_OTLP_ENDPOINT` to export traces; export failure does not fail the run. `agt doctor` is the setup check. It does not start the daemon.

## Not in this build

Kubernetes, a durable workflow engine, a separate vector database, multi-agent workers, a web console, Stripe, and automatic long-term memory.
