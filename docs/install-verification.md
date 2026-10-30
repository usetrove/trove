# Install and uninstall verification

Run this on a clean machine for each row, using only the README. Record real results; leave a cell blank until it has been measured. A "clean" machine means no Go toolchain needed, no earlier `~/.trove`, and no Trove config in any agent.

Automated checks already cover the install script and the npm wrapper (`sh install_test.sh`, `node --test "npm/test/*.test.js"`); this page is the by-hand check on real systems.

## Matrix

| Install path | macOS (arm64) | macOS (amd64) | Linux (amd64) | Linux (arm64) |
|---|---|---|---|---|
| Homebrew | | | n/a unless Linuxbrew | n/a unless Linuxbrew |
| `npx trove-cli doctor` | | | | |
| `install.sh` | | | | |
| Direct download + manual checksum | | | | |

For each cell, write: date, OS version, pass/fail, and the seconds from the first command to `trove version` printing.

## Per-path steps

1. Start from a fresh user account or VM snapshot.
2. Run the README command for the path. Note the time.
3. `trove version` prints the release version and a commit.
4. **Checksum is enforced.** Tamper with the downloaded archive (change one byte) or `checksums.txt` and repeat the install. It must stop with a plain-English error and must not run or install the binary.
5. **Signature (direct download).** Run the `cosign verify-blob` command from the README against the real release. It must succeed for the release tag and fail for a different tag in `--certificate-identity`.
6. `trove doctor` has no failures. A pricing-age warning is a release blocker.

## Time to first useful output

Use a machine with real Claude Code history on disk.

| Step | Time (seconds) |
|---|---|
| Start of install to `trove version` | |
| `trove` (history report) first output | |
| Start of install to first report, total | |

The target is under 2 minutes total. Report the measured time, whichever side of the target it lands on.

Gateway users: note the time from `trove setup` to the first recorded request (`trove report`). The target is within the first session.

## Uninstall leaves no trace

Do this on the same machine, after using Trove for real: `trove setup`, one `trove fix <id> --apply`, `trove hooks install`, `trove mcp install`, and `trove setup --status-line`. Before starting, save a copy of each agent config file and `shasum -a 256` them.

1. `trove uninstall --dry-run` lists what it will do. Read it.
2. `trove uninstall --purge-data`.
3. Agent config files match the saved copies byte for byte (or, if you had edited them since, only Trove's lines are gone).
4. Nothing is left of: `~/.trove`, the background service (`launchctl list | grep -i trove` on macOS, `systemctl --user list-units | grep -i trove` on Linux), the MCP registration (`claude mcp list`), hooks and `statusLine` entries.
5. Remove the program: `brew uninstall trove`, or delete the binary the install script placed. For `npx` users, delete `~/.cache/trove-cli`.
6. Check no listener remains: nothing on the gateway port.

`trove uninstall` does not remove the program itself or the npm cache; the README says so. If any other trace is found, it is a bug: file it and fix it before release.

## Results log

Add a dated row for each run: who ran it, the OS and agent versions, path, and anything that was not as the README described.

| Date | Who | OS | Path | Result | Notes |
|---|---|---|---|---|---|
