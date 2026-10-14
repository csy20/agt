# agt

Build and test from the repository root:

```sh
make test
```

`make test` starts Postgres, builds the Python, Go, and JavaScript sandbox images, and runs `go test ./...`. A `go test` without `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, and `AGT_IMAGE_NODE` can use the host runner inside tests. That is not container support. Production `agtd` has no host fallback.

A model can request an action. Deterministic Go decides authority. There is no shell tool. Model names and token prices come from environment variables (`AGT_MODEL_NAME`, `AGT_INPUT_USD_PER_M`, `AGT_OUTPUT_USD_PER_M`), not from source constants.

Repository files, tool output, and web pages are data. They are not copied into the system prompt.

The fake provider script is a labelled demo. Do not treat it as evidence that a live model fixed a repository. `git push` and opening a pull request are unsupported. Do not record them as successful. Production test execution stays in the Docker runner. There is no host fallback.

`agtd` binds a loopback address only. Every route except `GET /healthz` requires the local bearer token. Secret filenames are not copied, read, written, deleted, or patched. Redact tool output before it reaches a model. The sandbox mounts only the run workspace.

`agt fix` and `agt run create` require `--profile`. `--repo` does not select a profile. `agt profile detect` only prints a suggestion. The daemon fills the image, architecture, environment, network (`none`), and dependency mode (`prebuilt`). Do not accept an image from the client or from repository files. These profiles have no prepare step and do not enable the network. Empty test argv is an error. There is no production default of `python -m unittest tests.test_retry`.

`write_file` and `delete_file` are hash-preconditioned. `apply_patch` remains. Docker's default tmpfs is `noexec`; the run's `/tmp` must set `exec` so `go test` can run the test binary. Keep `nosuid` and keep the root filesystem read-only.

The model transcript is `transcript.v1` in `model_messages`. Keep repository summaries, plans, tool output, and verification output in user or tool messages tagged `DATA origin=`. Do not copy them into the system prompt. Accept one tool call per turn. Do not send `parallel_tool_calls`. A loopback base URL may omit the API key. Any other base URL requires `AGT_MODEL_API_KEY`. Do not call a hosted model unless that run was explicitly requested. A verification failure may return to observing while budget remains, and the sandbox stays open for that return. A repeated failure fingerprint or an exhausted verification budget stops the run and closes the sandbox. A repeated ineffective edit stops the run. Test-file edits require approval. Outcome `repaired` requires a failing baseline, a later edit that did not change a test file, and a passing verification.

The workspace is a snapshot plus `patch.v1`, not a Git worktree. Copy the working tree, including uncommitted files, and do not modify the source during the run. Reject a data directory nested with the selected repository. `agt run apply` is the only local write to that repository. It requires `SUCCEEDED` and a digest of the daemon-built artifact. Do not apply from the supervisor. Do not accept a client-selected target path. A stale touched file writes nothing. `COMMITTING` is not a Git commit.
