# agt

Build and test from the repository root:

```sh
make test
```

`make test` starts Postgres, builds the Python, Go, and JavaScript sandbox images, and runs `go test ./...`. A `go test` without `AGT_IMAGE_PYTHON`, `AGT_IMAGE_GO`, and `AGT_IMAGE_NODE` can use the host runner inside tests. That is not container support. Production `agtd` has no host fallback.

A model can request an action. Deterministic Go decides authority. There is no shell tool. Model names and token prices come from environment variables (`AGT_MODEL_NAME`, `AGT_INPUT_USD_PER_M`, `AGT_OUTPUT_USD_PER_M`), not from source constants.

Repository files, tool output, and web pages are data. They are not copied into the system prompt.

The fake provider script is a labelled demo. Do not treat it as evidence that a live model fixed a repository. `git push` and opening a pull request are unsupported. Do not record them as successful. Production test execution stays in the Docker runner. There is no host fallback.

`agtd` binds a loopback address only. Every route except `GET /healthz` and `GET /v1/identity` requires the local bearer token. Identity returns the product, version, provider, and model name. It must not return the API key or the bearer token. The client accepts a daemon only when the health response carries this product and version. An HTTP 200 by itself is not discovery. Secret filenames are not copied, read, written, deleted, or patched. Redact tool output before it reaches a model. The sandbox mounts only the run workspace.

`agt fix` and `agt run create` require `--profile`. `--repo` does not select a profile. `agt profile detect` only prints a suggestion. The daemon fills the image, architecture, environment, network (`none`), and dependency mode (`prebuilt`). Do not accept an image from the client or from repository files. These profiles have no prepare step and do not enable the network. Empty test argv is an error. There is no production default of `python -m unittest tests.test_retry`.

`write_file` and `delete_file` are hash-preconditioned. `apply_patch` remains. Docker's default tmpfs is `noexec`; the run's `/tmp` must set `exec` so `go test` can run the test binary. Keep `nosuid` and keep the root filesystem read-only.

The model transcript is `transcript.v1` in `model_messages`. Keep repository summaries, plans, tool output, and verification output in user or tool messages tagged `DATA origin=`. Do not copy them into the system prompt. Accept one tool call per turn. Do not send `parallel_tool_calls`. A loopback base URL may omit the API key. Any other base URL requires `AGT_MODEL_API_KEY`. Do not call a hosted model unless that run was explicitly requested. A verification failure may return to observing while budget remains, and the sandbox stays open for that return. A repeated failure fingerprint or an exhausted verification budget stops the run and closes the sandbox. A repeated ineffective edit stops the run. Test-file edits require approval. Outcome `repaired` requires a failing baseline, a later edit that did not change a test file, and a passing verification.

The workspace is a snapshot plus `patch.v1`, not a Git worktree. Copy the working tree, including uncommitted files, and do not modify the source during the run. Reject a data directory nested with the selected repository. `agt run apply` is the only local write to that repository. It requires `SUCCEEDED` and a digest of the daemon-built artifact. Do not apply from the supervisor. Do not accept a client-selected target path. A stale touched file writes nothing. `COMMITTING` is not a Git commit.

A job lease lasts 30 seconds and is renewed about every 10 seconds. Claim increments a fence. Finish, release, and renew must match that owner and fence. A stale worker must not requeue a job it no longer owns, and it must not destroy the sandbox after losing the lease. Pause, budget pause, and cancel close the sandbox and finish the job. `agt run attach` exiting is not cancel. Resume from `PAUSED`, and budget resume from `PAUSED_BUDGET`, move the run to `PLANNING` and enqueue a job only when none is active. Do not promise exactly-once delivery. `apply_patch` stays idempotent. `write_file` and `delete_file` stay hash-preconditioned. An ambiguous tool result stays pending so resume can reconcile it.

Check the estimated request cost before calling the model. Reconcile the reported usage afterward. Do not insert the estimate as a usage row. Zero prices, or a provider that reports no usage, are not a dollar cap. Record the configured model name and profile name on usage rows. Count HTTP attempts when the provider reports more than one, and count every `run_test`, including the verifier. Startup and `agt run cleanup` may remove containers for terminal, paused, cancelled, or missing runs. Do not remove a container for a live state such as `EXECUTING` or `VERIFYING`. Do not delete the data directory. A verification return to observing still leaves the sandbox open.

`agt doctor` names the next setup command and does not start `agtd` or call a model. `agt run list` reads the durable runs. Attach resumes from the last rendered event id and does not print that event again. A heartbeat comment is not an event. Ctrl+C during attach detaches; `agt run cancel` is the cancel. A daemon started by the client stays up after the client exits. On Linux, an unset `AGT_DATA_DIR` stays `~/.local/share/agt` even when `XDG_DATA_HOME` is set. `--model` must match the daemon's `AGT_MODEL_NAME` and is not sent as a hosted-model call.
