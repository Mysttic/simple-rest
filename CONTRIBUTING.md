# Contributing

Building, tests, CI and releases. How to use the program is in the [README](README.md).

## Building

Only Go is needed (https://go.dev/dl/), with no CGO and no external modules. The version number
is compiled in from `VERSION.md`.

```
go build -ldflags "-s -w" -o simple-rest.exe .
```

A whole release into `dist/` (three binaries and a file with the SHA-256 sums):

```
sh build.sh        # Linux/macOS/Git Bash
build.cmd          # Windows cmd
```

For a system outside these three, give Go the target, macOS on Apple Silicon for instance:

```
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w" -o simple-rest .
```

A copy that reports another number, for testing the updater:
`go build -ldflags "-X main.versionOverride=0.9.0" -o simple-rest.exe .`

## Development and tests

Branches: `develop` (work) and `master` (releases). CI does not run on `develop`, so run the
tests locally (commands below) or by hand from the Actions tab (`workflow_dispatch`); only a pull
request `develop` → `master` is checked automatically.

```
go test ./... -count=1                 # server, proxy, guard, updater (with a fake hub), schedules, TCP
go test -race ./... -count=1           # the same with the race detector (needs gcc); CI does not do this
node --test "tests/**/*.test.mjs"      # UI: params/URL, variables, auth, Postman import, cURL, saving, schedules, TCP
sh scripts/e2e-update.sh               # the update path end to end on a real binary
node scripts/screenshots.mjs           # screenshots into site/simple-rest/img/ (Edge or Chrome)
```

The UI tests load the script from `ui.html` through `tests/load-ui.mjs`, without a browser:
`tests/ui.test.mjs` checks the pure functions, `tests/state.test.mjs` saving and syncing the
data file, and `tests/schedules.test.mjs` the schedules window, the overlay and Send with fresh
values. The program builds a schedule's request in Go (`effective.go`), as the page builds the
request of the Send button. Shared vectors in `testdata/effective.json` run in
`effective_test.go` and in `tests/effective.test.mjs`, so both sides build the same request from
the same data. A change to one of these functions needs a change to its counterpart and to the
vectors.

`tests/tcp.test.mjs` checks the TCP console: the message editor and the bytes that come out of
it, what the tab keeps from the program's events, the fall back from the event stream to polling,
and the console's actions. Framing and the host and port checks have counterparts in Go
(`tcpframe.go`, `tcp.go`), and shared vectors in `testdata/tcp-framing.json` run in
`tcpframe_test.go` and in `tests/tcp.test.mjs`. `tcp_linux_test.go` (`TCP_USER_TIMEOUT`) runs on
Linux only, so from Windows it runs only in CI on the release pull request, or on a Raspberry Pi.

`scripts/screenshots.mjs` builds the app, starts it with a sample collection and a second copy
that reports an older version, and takes the screenshots over the DevTools protocol. A fake hub
offers a release with the number from `VERSION.md` and hands out a token that a schedule turned
on by the script keeps fresh. So the pictures are real and can be made again after every change
to the UI. The promo screenshot sends a real request to `api.github.com`, so the script needs the
network.

The `CI` workflow (`.github/workflows/ci.yml`) runs all of this, together with `gofmt` and
`go vet`, only on a pull request from `develop` into `master`. A push to `develop` and a pull
request into `develop` start nothing; a manual run from the Actions tab (`workflow_dispatch`)
stays for emergencies. A pull request into `master` from a branch other than `develop` skips
every job, so GitHub shows such a run as green. That is a deliberate decision from the time the
repository was private and short of Actions minutes, not an oversight. The build of every
platform (`sh build.sh`) stays in CI, because on a Linux runner `go vet` and `go test` do not
compile `restart_windows.go`, so it is the only step that checks the windows/amd64 and
linux/arm64 targets; only the upload of `dist/` as a run artifact went. A pull request from
`develop` also gets a "Release preview" job that says whether the merge publishes a release.
Changes to documentation only (`README.md`, `README.pl.md`, `CONTRIBUTING.md`, `docs/`,
`site/`, `LICENSE`) do not start CI.

Nothing in the repository runs on a schedule any more, and nothing cleans the Actions storage.
No run uploads artifacts now, but the logs of every run still count towards the account's
storage limit. If it fills up, clear it by hand: from the Actions tab or with
`gh api -X DELETE /repos/Mysttic/simple-rest/actions/runs/<id>/logs`.

## Releases

- `VERSION.md` holds the version number (one line, `major.minor.patch`). The program compiles it
  in when it is built, so the binary and the tag always agree.
- `CHANGELOG.md` describes the changes of every version (in the Keep a Changelog format). A
  release needs a `## [x.y.z]` section for the number in `VERSION.md`; its text becomes the
  description of the release.
- The `Release` workflow (`.github/workflows/release.yml`) starts after a push to `master` that
  changes `VERSION.md` (that is, after merging a pull request from `develop` with a raised
  number). A change to `CHANGELOG.md`, to the code or to the workflow alone while the number is
  not released yet (after a failed run, for instance) starts nothing and needs a manual "Run
  workflow" on `master`, and on `master` only: from another branch the workflow skips every
  job. It publishes only when `VERSION.md` is above the newest `v*` tag. Then come the tests,
  the build, the tag `v<version>` and a release in this repository with the files from `dist/`,
  and after that the `site` job copies the same files to Mysttic/Mysttic under the tag
  `simple-rest-v<version>` and deletes the project's older releases there. Without a raised
  `VERSION.md` a merge publishes nothing. A lowered number stops the workflow with an error.
  There is no full CI here: the same tests already ran on the pull request `develop` → `master`,
  on the same tree. A short `Tests` job (`go test` and the UI tests, about 2 minutes) stays
  before the build: `master` is not a protected branch, so a push straight to `master` bypasses
  the pull request, and the `site` job deletes the previous release from the site for good, with
  nothing to go back to.

Release procedure:

1. On `develop`: raise `VERSION.md` and add a section to `CHANGELOG.md`.
2. A pull request `develop` → `master`; wait for green CI and the "Release preview".
3. Merge. The release and the copy of its files to the hub happen by themselves.
4. When a release changes the project page (description, screenshots), copy the page to the hub
   by hand, see `site/README.md`.

Required secret: `HUB_TOKEN` in the repository settings (Settings, Secrets and variables,
Actions): a fine-grained token for the owner `Mysttic`, with access to `Mysttic/Mysttic` only
and the permission Contents: Read and write. Without it the release in this repository is made,
and the `site` job ends with a readable error.

The project page for the hub is ready in `site/simple-rest/`; `site/README.md` describes how to
hook it up and refresh it. The `site` job copies only the release files to the hub, not the
content of the page.
