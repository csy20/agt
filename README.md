# agt

`agt` is a local agentic terminal for one workflow: make a failing test pass, show the diff, and stop before any remote side effect. The CLI is a client. `agtd` is the control plane. It accepts only a loopback `AGT_LISTEN`: `127.0.0.1`, another address in `127.0.0.0/8`, `::1`, or the literal name `localhost`. `0.0.0.0`, an empty host, and other interfaces are rejected at startup.

A run is the durable object in Postgres. The model proposes a tool call. Policy allows it, denies it, or asks for an approval bound to that command's argument hash. Tests run in a per-run container with no network. `git push` and opening a pull request do not spawn a process in this build, even after the hash matches.

The fake provider script lives in `eval/scripts/`, outside the fixture repository, so a search of the repo cannot see the answer. It is a labelled demo. `agt` does not load it unless `--script` is passed, and it is not a live model. `--provider` defaults to `AGT_PROVIDER`. There is no silent provider.

## Demo

```sh
make demo
```

With Postgres and `agtd` already running:

```sh
./bin/agt fix --repo eval/fixtures/payrepo --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --provider fake --script eval/scripts/fix_retry.json "make the retry test pass"
```

`--profile` is required. `--repo` does not choose a language or a test command. `agt profile detect --repo PATH` prints a suggestion and does not start a run. The same flags work for the Go fixture (`--profile go --test-arg go --test-arg test --test-arg ./...`, repo `eval/fixtures/gocounter`) and the JavaScript fixture (`--profile javascript --test-arg node --test-arg --test`, repo `eval/fixtures/jsquote`). `typescript` is an alias of the JavaScript profile. These profiles do not install dependencies: the network stays disabled and the image already contains the toolchain.

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

The copied workspace keeps executable bits and project files such as `.github` and `.gitignore`. It skips `.git`, `node_modules`, `vendor`, and common build directories (`build`, `dist`, `target`, and similar). It also skips secret names such as `.env`, private keys, and the directories `.ssh`, `.aws`, `.gnupg`, `.kube`, and `.docker`. `.env.example`, `.env.sample`, and `.env.template` are copied. A file over 8 MiB fails the copy. Symlinks are refused. Paths that leave the worktree, including `..`, absolute paths, drive letters, and UNC paths, are rejected. A path containing `:` is rejected so a Windows alternate data stream cannot be addressed.

Unix hosts pass the host uid and gid to Docker. Native Windows uses the unprivileged image user `65532:65532`. The daemon does not pass `-1:-1`, and it does not run tests on the host when Docker is missing. The test container mounts only that run's workspace at `/workspace`. It does not mount the home directory, SSH or cloud credential directories, or the Docker socket, and it does not receive the host environment. The client sends a profile name and an argv. The daemon chooses the image. A request that includes an image is rejected. Python, Go, and JavaScript use `agt-sandbox:python`, `agt-sandbox:go`, and `agt-sandbox:node` unless `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, or `AGT_IMAGE_NODE` is set. `AGT_SANDBOX_IMAGE` is the Python image. Startup fails if a configured image is missing. `/tmp` in the container is executable because `go test` writes and runs its test binary there. The root filesystem stays read-only, and `/tmp` stays `nosuid`.

Every route except `GET /healthz` requires `Authorization: Bearer` with the token in `$AGT_DATA_DIR/auth.token`. On Unix the file is mode `0600`. A group- or world-readable token file is replaced. The client reads `AGT_TOKEN` when it is set, and otherwise reads that file, so the client and daemon need the same `AGT_DATA_DIR` unless `AGT_TOKEN` is set for both. Requests with a browser `Origin` or a non-loopback `Host` are rejected. Tool output is redacted before it is given to the model.

## Layout

| Path | Role |
|---|---|
| `cmd/agt` | CLI |
| `cmd/agtd` | Control plane and worker |
| `internal/policy` | Allow, deny, or require approval |
| `internal/sandbox` | Per-run Docker test execution |
| `internal/profile` | Python, Go, and JavaScript execution profiles |
| `eval/fixtures/payrepo` | Buggy Python sample |
| `eval/fixtures/gocounter` | Buggy Go sample |
| `eval/fixtures/jsquote` | Buggy JavaScript sample |
| `eval/scripts/` | Scripted model steps, kept outside the fixtures |

`agtd` reads `AGT_DATABASE_URL`, `AGT_SANDBOX_IMAGE`, `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, `AGT_IMAGE_NODE`, `AGT_LISTEN`, `AGT_DATA_DIR`, and `AGT_PROVIDER`. Set `AGT_TOKEN` to choose the local bearer token; otherwise `agtd` creates `$AGT_DATA_DIR/auth.token`. An OpenAI-compatible provider is selected with `--provider openai` plus `AGT_MODEL_BASE_URL`, `AGT_MODEL_API_KEY`, and `AGT_MODEL_NAME`. Set `AGT_OTLP_ENDPOINT` to export traces; export failure does not fail the run.

## Not in this build

Kubernetes, a durable workflow engine, a separate vector database, multi-agent workers, a web console, Stripe, and automatic long-term memory.
