# @usetrove/trove

Trove is the efficiency layer for AI coding agents. This package is the easy way to install it with npm:

```bash
npm install -g @usetrove/trove
trove
```

or run it once without installing: `npx @usetrove/trove`.

It works on macOS, Linux and Windows, on x64 and arm64. Node 18 or newer.

## What it does

The first time you run `trove`, the package downloads the Trove release binary for your platform from
[GitHub Releases](https://github.com/usetrove/trove/releases), checks its SHA-256 against the release's `checksums.txt`
(and refuses to run it on a mismatch), caches it, and runs it. After that it runs the cached copy. The package version is
the Trove version, so `@usetrove/trove@0.4.0` runs Trove 0.4.0.

- **No install script.** Nothing runs when you `npm install`, so it also works with `--ignore-scripts`.
- **Where the binary is cached:** `~/.cache/trove-npm` on macOS and Linux (or `$XDG_CACHE_HOME/trove-npm`), and
  `%LOCALAPPDATA%\trove-npm` on Windows.
- **What the checksum covers:** it protects against a corrupted or swapped download. It does not verify the cosign
  signature on `checksums.txt`; to check that too, see the main repository README, or use the install script, which does.

Source and full documentation: <https://github.com/usetrove/trove>
