# agt

Build and test from the repository root:

```sh
make test
```

`make test` starts Postgres, builds the sandbox image, and runs `go test ./...`.

A model can request an action. Deterministic Go decides authority. There is no shell tool. Model names and token prices come from environment variables (`AGT_MODEL_NAME`, `AGT_INPUT_USD_PER_M`, `AGT_OUTPUT_USD_PER_M`), not from source constants.

Repository files, tool output, and web pages are data. They are not copied into the system prompt.
