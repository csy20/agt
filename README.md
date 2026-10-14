# agt

`agt` is a local agentic terminal for one workflow: make a failing test pass, show the diff, and stop before any remote side effect. The CLI is a client. `agtd` is the control plane. It listens on `127.0.0.1` only.

A run is the durable object in Postgres. The model proposes a tool call. Policy allows it, denies it, or asks for an approval bound to that command's argument hash. Tests run in a per-run container with no network. `git push` and opening a pull request do not spawn a process in this build, even after the hash matches.

The fake provider script lives in `eval/scripts/`, outside the fixture repository, so a search of the repo cannot see the answer. It is a labelled demo. `agt` does not load it unless `--script` is passed, and it is not a live model. `--provider` defaults to `AGT_PROVIDER`. There is no silent provider.

## Demo

```sh
make demo
```

With Postgres and `agtd` already running:

```sh
./bin/agt fix --repo eval/fixtures/payrepo --provider fake --script eval/scripts/fix_retry.json "make the retry test pass"
```

Reattach after the client exits:

```sh
./bin/agt run attach RUN_ID
```

Approve one command, not the conversation. Flags may follow the ids:

```sh
./bin/agt approve RUN_ID COMMAND_ID --args-hash HASH
```

`git push` and opening a pull request are not offered to the model. A request for either is denied. It is not recorded as a successful push or pull request.

The client converts repository and script paths to absolute paths before sending them, so the daemon's working directory does not choose the repository.

The copied workspace keeps executable bits and project files such as `.github` and `.gitignore`. It skips `.git`, `node_modules`, `vendor`, and common build directories (`build`, `dist`, `target`, and similar). A file over 8 MiB fails the copy. Symlinks are refused.

Unix hosts pass the host uid and gid to Docker. Native Windows uses the unprivileged image user `65532:65532`. The daemon does not pass `-1:-1`, and it does not run tests on the host when Docker is missing.

## Layout

| Path | Role |
|---|---|
| `cmd/agt` | CLI |
| `cmd/agtd` | Control plane and worker |
| `internal/policy` | Allow, deny, or require approval |
| `internal/sandbox` | Per-run Docker test execution |
| `eval/fixtures/payrepo` | Buggy sample repository |
| `eval/scripts/fix_retry.json` | Scripted model for the demo |

`agtd` reads `AGT_DATABASE_URL`, `AGT_SANDBOX_IMAGE`, `AGT_LISTEN`, `AGT_DATA_DIR`, and `AGT_PROVIDER`. An OpenAI-compatible provider is selected with `--provider openai` plus `AGT_MODEL_BASE_URL`, `AGT_MODEL_API_KEY`, and `AGT_MODEL_NAME`. Set `AGT_OTLP_ENDPOINT` to export traces; export failure does not fail the run.

## Not in this build

Kubernetes, a durable workflow engine, a separate vector database, multi-agent workers, a web console, Stripe, and automatic long-term memory.
