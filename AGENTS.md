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
