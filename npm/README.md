# trove-cli

`npx trove-cli` downloads the Trove release binary for your platform from GitHub Releases,
verifies its SHA-256 against the release's `checksums.txt`, caches it in `~/.cache/trove-cli`, and runs it.

The checksum protects against corrupted or swapped downloads. To also verify the cosign
signature on `checksums.txt`, see the main repository README.
