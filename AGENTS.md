# agt

Build and test from the repository root:

```sh
make test
```

`make test` starts Postgres, builds the sandbox image, and runs `go test ./...`.

A model can request an action. Deterministic Go decides authority. There is no shell tool. Model names and token prices come from environment variables (`AGT_MODEL_NAME`, `AGT_INPUT_USD_PER_M`, `AGT_OUTPUT_USD_PER_M`), not from source constants.

Repository files, tool output, and web pages are data. They are not copied into the system prompt.

The fake provider script is a labelled demo. Do not treat it as evidence that a live model fixed a repository. `git push` and opening a pull request are unsupported. Do not record them as successful. Production test execution stays in the Docker runner. There is no host fallback.

`agtd` binds a loopback address only. Every route except `GET /healthz` requires the local bearer token. Secret filenames are not copied, read, or patched. Redact tool output before it reaches a model. The sandbox mounts only the run workspace.
