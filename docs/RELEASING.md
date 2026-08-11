# Releasing

One file decides the version, and merging to `main` ships it.

```
VERSION          0.1.0
```

Bump `VERSION` in the pull request. When it merges to `main`, CI tags
`v0.1.1` (or whatever you wrote), builds every platform, and publishes a
GitHub Release with the archives and their checksums. Leave `VERSION`
alone and the tag already exists, so nothing is published — merging docs
or a fix you are not ready to ship costs nothing.

## Choosing the number

`MAJOR.MINOR.PATCH`.

| bump | when |
|---|---|
| **major** | a build's output changes in a way a consumer must react to — a removed or renamed property, a changed contract, a different meaning for an existing field |
| **minor** | new capability that existing callers can ignore: a new command, a new flag, a new emitted property, a new input format |
| **patch** | a fix, a performance change, or a documentation change, where every existing caller keeps working unchanged |

There is no separate "hotfix" component. A hotfix is a patch release cut
from `main` — the urgency is a property of the branch you cut from, not
of the number.

Portolan is pre-1.0, so the map's geometry is still allowed to move
between minor versions. Say so in the release notes when it does: a
downstream renderer that pins pixel diffs cares more about that than
about the API.

## What ships

Six archives, one per target:

| target | archive |
|---|---|
| macOS Apple silicon | `portolan_<v>_darwin_arm64.tar.gz` |
| macOS Intel | `portolan_<v>_darwin_amd64.tar.gz` |
| Linux x86-64 | `portolan_<v>_linux_amd64.tar.gz` |
| Linux arm64 | `portolan_<v>_linux_arm64.tar.gz` |
| Windows x86-64 | `portolan_<v>_windows_amd64.zip` |
| Windows arm64 | `portolan_<v>_windows_arm64.zip` |

Each holds the `portolan` binary, the `style/` curation documents, the
README, the LICENSE and the reference docs. About 3 MB compressed.

Portolan is pure Go with no module dependencies and embeds its own
assets, so every target cross-compiles from one Linux runner with
`CGO_ENABLED=0`. There is no per-platform runner, no toolchain to
install, and no code signing — which is why this pipeline is one short
job rather than parchment's matrix of macOS and Android builders.

`style/` ships **with** the binary because `portolan` looks for `./style`
by default. A download without it draws every city in raw feed colours
and looks broken.

### What consumers can rely on

These are contracts, not incidentals — a packaged consumer's fetch step
breaks if they change, so change them only with a major bump.

- **Asset names carry the bare version, not the tag**:
  `portolan_0.1.0_darwin_arm64.tar.gz`, with no leading `v`. In a
  workflow that is `${TAG#v}`.
- **An archive unpacks to a directory**, `portolan_<version>_<os>_<arch>/`,
  holding the binary plus `style/`, `README.md`, `LICENSE` and `docs/` —
  not a bare binary at the root.
- **`SHA256SUMS` hashes the ARCHIVES, not the binaries inside them**, in
  coreutils `sha256sum` format (hash, two spaces, bare filename). So a
  consumer verifies before extracting, which is the right order. Verify
  with `sha256sum -c SHA256SUMS`.
- **`portolan version` is for humans**; a program should ask a running
  server `GET /version`, which returns the bare semver and the PLNB
  layout version as JSON.

### A naming mismatch to expect

Go's `GOARCH` is `amd64` and `arm64`. Several packaging tools — Electron
and electron-builder among them — call the same architectures `x64` and
`arm64`. A consumer fetching `portolan_<v>_darwin_amd64.tar.gz` into a
`darwin-x64` resource directory has to map that explicitly. Portolan
uses Go's names because it is a Go binary and `GOOS`/`GOARCH` are what
built it; the mapping belongs on the packaging side.

### What does not ship

The **workbench** (`portolan atlas`) serves its dashboard from `web/dist`
on disk, deliberately — it is a development tool. The released binary
runs `atlas`, but its `/console` needs a repo checkout with `web/dist`
built. The release is the CLI and the build server.

## The pipeline

| workflow | trigger | what it does |
|---|---|---|
| `ci.yml` | every PR, pushes to `main` and `dev` | build, vet, test, report formatting |
| `tag-release.yml` | push to `main` | read `VERSION`, create the tag, call the release |
| `release.yml` | that call, or any `v*` tag push | test, cross-compile six targets, publish |

Tests run again inside `release.yml` before anything is built, so a
commit that reached `main` without a PR still cannot ship broken.

No secrets are needed. `GITHUB_TOKEN` creates the release.

## When a release does not happen

Two ways it can silently not run, both learned in the parchment repo and
both handled here:

1. **A CI-skip directive swallows the push.** GitHub skips the whole push
   event when the commit message contains one — and squash-merging
   concatenates every branch commit message into the merge body. One
   routine `skip ci` on a branch commit is enough, with no run and no
   failure to notice.

2. **The tag already exists.** A re-run finds it and no-ops, so a version
   whose build failed can never be retried.

For both: run **Tag Release** manually from the Actions tab with
`force` checked. It releases the version in `VERSION` even when the tag
is already there.

## Building locally

```bash
make dist          # every target into dist/, archives and checksums
```

Same flags as CI, so what you get locally is what ships. `portolan
version` reports the stamped number; a plain `go build` says `devel`
rather than claiming a release it is not.

## The first release

`VERSION` starts at `0.1.0` and `main` has not been used as a release
branch before — it still points at the pre-rewrite tree. Bring `main`
up to date with `dev` once, and that merge cuts `v0.1.0`.

## The npm client

`clients/npm` publishes [`@alexwohlbruck/portolan`](https://www.npmjs.com/package/@alexwohlbruck/portolan):
a typed wrapper that spawns the engine, speaks the build API and decodes
PLNB. **The engine ships inside it**, one binary per platform as an
`optionalDependency`, so a consumer gets a new engine by bumping one
dependency.

Its version is the engine's version — they are published together, and
the wrapper pins its six platform packages to the exact same number. A
wrapper free to resolve a different engine could pair a decoder with a
PLNB layout it does not understand, which is the one failure the version
handshake exists to prevent.

```bash
make dist                                    # the six archives
cd clients/npm
node scripts/build-platform-packages.mjs     # binaries -> platforms/
npm test                                     # builds a real engine and drives it

for d in platforms/*/; do (cd "$d" && npm publish --access public); done
npm publish --access public                  # the wrapper LAST
```

Publish the platform packages **before** the wrapper, and do not skip
`build-platform-packages.mjs` after a version bump. Both mistakes have
the same shape and it is a nasty one: npm **skips an optional dependency
it cannot resolve and still reports the install succeeded**. A consumer
sees `added 1 package, found 0 vulnerabilities`, a lockfile that looks
right, and a missing-binary error at their first chart call — pointing
at their install flags rather than at this release.

So `prepublishOnly` runs `scripts/check-publish.mjs`, which refuses to
publish unless `VERSION`, the wrapper, and all six staged platform
packages agree, **and** all six versions already exist on the registry.
It is the runbook above, enforced. `PORTOLAN_SKIP_REGISTRY_CHECK=1`
drops only the registry leg, for an offline dry run.

`node scripts/build-platform-packages.mjs` reads the release archives
rather than invoking the Go toolchain, so what npm carries is
byte-identical to what the GitHub release carries.

Publishing is deliberately **not** wired into `release.yml`. It needs an
`NPM_TOKEN`, and an npm publish cannot be undone the way a GitHub
release can — unpublishing is restricted and republishing a version is
forbidden outright.
