# Security policy

Trove handles API traffic in memory and is designed never to store or log credentials. What it stores and
where is listed in [PRIVACY.md](PRIVACY.md). Every security control, what it does not protect against, and the
test that holds each one in place are in [docs/security.md](docs/security.md). The attackers considered are in
[docs/threat-model.md](docs/threat-model.md).

## Supported versions

Only the latest release gets security fixes. Trove is pre-1.0, so a fix ships as a new patch release rather than
as a backport. Update with the install command in the README, or `brew upgrade trove`.

## Reporting a vulnerability

Please report privately through GitHub's "Report a vulnerability" feature on this repository (Security tab). Do
not file public issues for security problems. Include steps to reproduce, the Trove version (`trove --version`)
and your operating system, and never include real API keys or private code.

What to expect (these are targets, not guarantees):

- an acknowledgement within 5 working days;
- a first assessment (accepted, needs more information, or not a vulnerability) within 10 working days;
- a fix or a mitigation for a confirmed high-severity issue within 30 days, and for lower severities in the next
  planned release;
- credit in the release notes if you want it, and coordinated disclosure: please give us the time above before
  publishing.

In scope: the Trove binary, its installers, the Claude Code plugin, and anything that makes Trove store or send a
credential, read or write outside its own folder, run code the user did not approve, or accept a request from
something other than the local user. Out of scope: a process that already runs as the user and can edit their files
(it can do anything Trove can), vulnerabilities in the agents or providers themselves, and findings that need the
user to point Trove at a malicious upstream on purpose.

## Verifying a release

Every release publishes `checksums.txt` (a SHA-256 for each archive), a keyless cosign signature for it
(`checksums.txt.sigstore.json`) and one SPDX SBOM per archive. To check a download by hand, replace `X.Y.Z`:

```sh
cosign verify-blob \
  --certificate-identity 'https://github.com/usetrove/trove/.github/workflows/release.yml@refs/tags/vX.Y.Z' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

`install.sh` does the same when `cosign` is installed. Set `TROVE_REQUIRE_SIGNATURE=1` to make it refuse to
install unless the signature verifies. Without cosign it checks the checksum only and says so. (The npm wrapper
checks the checksum only.)

Two things this does not prove: that a release is bit-for-bit reproducible from source (configured, not yet
demonstrated), and that the code is free of bugs. Run `trove doctor` to see file permissions and what the
installed version does.
