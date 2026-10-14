# Local beta 0.1.0

This is a reviewable local package of `agt` and `agtd`. It is not published. It is not an external beta for macOS, Windows, or linux/arm64. The only execution evidence is Linux amd64.

The repository has no `LICENSE` file. These instructions do not present it as licensed.

`scripts/package.sh` writes unsigned archives, `SHA256SUMS`, and `BUILD.txt` under `dist/`. `dist/` is gitignored. Do not commit the binaries. The script runs `agt version` for linux/amd64. The other archives are cross-compiled and are not executed.

## Requirements

- Docker, so tests run in the sandbox. The daemon does not fall back to a host shell.
- Postgres 16. `deploy/compose.yaml` publishes `127.0.0.1:54329` for database `agt`, user `agt`, password `agt`.
- The three images `agt-sandbox:python`, `agt-sandbox:go`, and `agt-sandbox:node`, built from `deploy/python.Dockerfile`, `deploy/go.Dockerfile`, and `deploy/node.Dockerfile`.
- Go 1.23 or newer only if you build from source. The packaged binaries do not need a Go toolchain.
- A model name in `AGT_MODEL_NAME` when the provider is not `fake`. A hosted model was not called for this beta. Do not treat the fake-provider batch as a live-model result.

## Fresh machine

From a source checkout:

```sh
docker compose -f deploy/compose.yaml up -d --wait
make image
make build
```

`AGT_DATABASE_URL` defaults to the compose URL when you use the Makefile. Start the daemon with a private data directory:

```sh
mkdir -p "$HOME/.local/share/agt"
chmod 700 "$HOME/.local/share/agt"
AGT_DATA_DIR="$HOME/.local/share/agt" AGT_PROVIDER=fake AGT_MODEL_NAME=fake ./bin/agtd
```

In another terminal, with the same `AGT_DATA_DIR`:

```sh
AGT_DATA_DIR="$HOME/.local/share/agt" ./bin/agt doctor
```

Doctor names the next command. It does not start `agtd` and does not call a model. On Linux the data directory stays `~/.local/share/agt` when `AGT_DATA_DIR` is unset, even if `XDG_DATA_HOME` is set.

A repair still needs `--profile` and an explicit test argv. The source repository stays unchanged until you run `agt run apply` with the printed digest.

## Upgrade

Stop `agtd` with SIGINT or SIGTERM. Replace `agt` and `agtd`. Start `agtd` again. It applies SQL migrations on startup and keeps the existing data directory. Run `agt doctor` before the next repair. Do not delete the data directory as part of an upgrade.

## Uninstall

Stop `agtd`. Remove the binaries you copied. To delete local state, remove the data directory (`~/.local/share/agt` on Linux, or `$AGT_DATA_DIR`). That removes the token, the log, and run snapshots. `docker compose -f deploy/compose.yaml down` stops Postgres. Adding `-v` also deletes the database volume. `docker rmi agt-sandbox:python agt-sandbox:go agt-sandbox:node` removes the sandbox images. Those image and volume deletions are optional.

## Tests

`make unit` runs the packages in `scripts/unit-packages.txt` with the database and image variables cleared. It does not start Postgres or Docker. `TestSandboxRunsTestsAndStaysClosed` skips in that mode. The skip is not container support.

`make test` starts Postgres, builds the three images, sets `AGT_INTEGRATION=1`, and runs `go test ./...`. A missing database, Docker daemon, or image fails that job.

On Windows, `scripts/test.ps1` is the development entry:

```powershell
powershell -File scripts/test.ps1 -Mode unit
powershell -File scripts/test.ps1 -Mode integration
```

That script was not executed on the Linux host that added it. The Makefile `demo` target is a POSIX script. It is not the Windows path.

## Operating systems

| OS | Arch | What this host did |
|---|---|---|
| Linux | amd64 | `make unit`, `make test`, and `agt version` on the packaged binary |
| Linux | arm64 | Cross-compiled archive only |
| macOS | amd64 | Cross-compiled archive only |
| macOS | arm64 | Cross-compiled archive only |
| Windows | amd64 | Cross-compiled archive only |

Windows ACL and junction behavior, Docker Desktop's uid `65532`, and native macOS bind mounts were not executed.
