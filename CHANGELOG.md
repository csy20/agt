# Changelog

## Unreleased

Not a published release. The product version stays `0.1.0` so this client still matches `agtd`.

- Interactive session: `agt` on a terminal, `agt tui`, and `agt tui --dump`. The dump is example data and does not change a repository. Approvals stay bound to the argument hash. Apply stays bound to the digest. Ctrl+C leaves the screen and does not cancel or apply.
- Ask and plan are stored on the run. The daemon denies writes, test runs, and other mutations in those modes, including a create that bypasses the session. They finish with outcome `answered` and do not run repair verification. An empty interaction stays `agent`. `risk_level` is unchanged. Migration `005_interaction.sql` adds the column.
- Assistant text can be shown while the response is read. A tool call is still returned only after the body is complete and the arguments are valid JSON. An interrupted stream that has already shown text is not retried. Reconnecting does not repeat the edit.
- Approval and apply keys act on the focused confirm line. Composer text does not approve or apply. Routine hashes and digests are abbreviated. `ctrl+f` and the apply confirmation show the full values. The cent limit is labelled an estimate. Untrusted terminal controls are stripped from rendered text. Demo scenes stay out of a live session.
- `agt setup --confirm` records a profile and a test command. It does not select an image or start a run.
- Sessions are stored apart from runs. Migration `006_sessions.sql` adds `sessions`, ordered `session_messages`, and a nullable `runs.session_id`. A standalone run still works. `agt session list|new|open|show|submit` and `/sessions` reopen a session. An active session attaches to its existing run. A finished session shows its history. Neither starts work, replays a tool, or applies a patch. A repeated client message id does not create a second message or run. A new prompt while a run is active is refused and does not change that goal. A session stays bound to its canonical repository path. A new `/fix` uses a fresh snapshot. An unapplied patch stays pending, and the note says its edits are absent from that snapshot.
- A fake-provider result and its usage row record the configured model name when one is set. The provider stays the scripted fake provider. An unset model name stays empty. The provider string is not stored as the model name.

## 0.1.0

Local reviewable beta. Not published. Not an external beta for macOS, Windows, or linux/arm64.

This repository has no `LICENSE` file and is not presented as licensed.

- Fail closed when `AGT_INTEGRATION=1` and Postgres, Docker, or a sandbox image is missing. `make unit` stays dependency-free. `scripts/test.ps1` is the PowerShell entry and was not executed here.
- Score 20 scripted fake-provider repairs across Python, Go, and JavaScript, including a recovery, an approved test-file edit that passes the in-repo test and fails a hidden check, and an honest verification failure. This is not a hosted-model evaluation.
- `scripts/package.sh` writes unsigned `agt` and `agtd` archives, checksums, and `dist/BUILD.txt`. linux/amd64 is executed only as `agt version`. The other archives are cross-compiled and were not executed.

The earlier milestones on this line of work added loopback auth, explicit Python/Go/JavaScript profiles, a persisted repair transcript, a digest-bound local apply, job leases, and daemon discovery. A hosted model was not called. Applying a patch remains explicit. A cost budget is not a hard dollar cap when prices are zero or usage is unreported. Tool execution is not exactly-once.
