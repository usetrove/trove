# Publishing @usetrove/trove (maintainers)

The package is published by the `npm` job in `.github/workflows/release.yml` when a `v*` tag is pushed, after the GitHub
release and its archives exist (the package downloads them on first run). The package version is set from the tag, and a
pre-release tag such as `v0.5.0-rc1` is published under the `next` tag so it never becomes what `npm install` gets by default.
This file is not part of the published package (`package.json` lists only `bin`, `lib` and `README.md`).

## One-time setup

1. **Create the npm scope.** On npmjs.com, create an organization named `usetrove` (free for public packages). The package name
   `@usetrove/trove` is unclaimed at the time of writing; the unscoped names `trove` and `trove-cli` belong to unrelated projects,
   which is why this package is scoped.
2. **Create a token.** In npm, create a granular access token with read and write access to packages in the `usetrove` scope.
   Turn on two-factor authentication for the account first.
3. **Add it to GitHub.** In the `usetrove/trove` repository, add the token as an Actions secret named `NPM_TOKEN`.
   Until that secret exists the `npm` job skips itself, so a release never fails for want of it.

## Publishing the first version by hand (optional)

The next tagged release publishes it automatically. To put the current release on npm sooner:

```bash
cd npm
npm version 0.4.0 --no-git-tag-version      # match the release you want to expose
npm test
npm publish --access public                 # needs `npm login` as a member of the usetrove org
```

Do not commit the version bump: `package.json` keeps `0.0.0` in the repository, and the release job sets the real version.

## Checks after publishing

```bash
npm view @usetrove/trove version
npx --yes @usetrove/trove --version         # downloads the release binary, verifies it, prints the version
```

## Limits

The package verifies the SHA-256 of the archive it downloads against the release's `checksums.txt`. It does not verify the
cosign signature on `checksums.txt` (finding F-15); `install.sh` and `install.ps1` do when cosign is installed.
