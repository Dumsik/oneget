# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`oneget` is a Go CLI (module `github.com/v8platform/oneget`, Go 1.16) that downloads 1C:Enterprise
platform distributions and related packages from `https://releases.1c.ru`, authenticating through
`https://login.1c.ru`. It can optionally extract downloaded archives and serve the download
directory over HTTP. All user-facing CLI text (help, flag descriptions, log messages) is in Russian
— match that convention when touching `cmd/`.

## Build, test, run

```shell
go build ./...                       # build
go test ./...                        # run all tests
go test -run TestName ./downloader   # run a single test in a package
go test -coverprofile=coverage.out ./...
go mod tidy                          # required before build in CI (see .github/workflows/test.yml)
```

CI (`.github/workflows/test.yml`) runs on Linux and Windows against Go 1.16 and just does
`go mod tidy` + `go test`. There is no lint step configured.

Running locally requires 1C portal credentials:

```shell
export ONEC_USERNAME=user
export ONEC_PASSWORD=password
go run . get --path ./tmp/dist/ platform@8.3.18.1334
```

Releases are built via GoReleaser (`.goreleaser.yaml`, invoked through `goreleaser.sh`/`goreleaser.bat`);
Docker image publishing is currently disabled (commented out in `.goreleaser.yaml`).

## Architecture

The pipeline for the `get` command flows through four packages:

1. **`cmd/`** (`cmd/get.go`, `cmd/commands.go`) — urfave/cli command definitions and argument
   parsing. This is where the `RELEASE` argument mini-language
   (`project[:filter.[filter]...]@version[:filter]`) gets tokenized into `dloader.DownloadConfig`
   values — one per project/version pair. It also drives post-download extraction (via `unpacker`)
   and optionally starts the HTTP file server (via `http-server`). New CLI commands are registered
   in the `cmd.Commands` slice in `cmd/commands.go`.

2. **`downloader/`** — the core logic, split across four files with distinct responsibilities:
   - `client.go`: `Client` handles auth against `login.1c.ru`/`releases.1c.ru` (ticket-based login,
     cookie jar, automatic re-auth on `401`).
   - `html.go`: `HtmlParser` scrapes releases.1c.ru HTML pages (goquery) into `ProjectInfo` /
     `ProjectVersionInfo` / `ReleaseFileInfo`. Parsing logic is coupled to the site's current markup
     — if downloads silently return nothing, this is the first place to check.
   - `filter.go`: `FileFilter` (matches a release *file* by name/URL, e.g. `win`, `deb`, `thin`) and
     `VersionFilter` (selects which *release versions* to include, e.g. `latest`, `from:date`,
     `from-v:version`, arbitrary regexp) implementations, one per project family (Platform, EDT,
     PostgreSQL, Executor — each has its own filter semantics and quirks, see README). Project name
     aliases (`platform`→`platform83`, `edt`→`DevelopmentTools10`, etc.) are resolved here via
     `GetProjectIDByAlias`.
   - `oneDownloader.go`: `OnegetDownloader.Get` orchestrates: fetch project release list → filter by
     `VersionFilter` → for each matching release, fetch its file list → filter by `FileFilter` +
     additional regexp filters → download concurrently (bounded by a `limit` channel of 10, tracked
     via `sync.WaitGroup`) into `BasePath/<project>/<version>/<filename>`. Downloads land in a
     `.d1c`-suffixed temp file first, then get renamed atomically on success — resuming a previous
     run skips files that already exist at the final path.

3. **`unpacker/`** — thin wrapper over `mholt/archiver/v3` for extracting downloaded distributions,
   plus `GetAliasesDistrib`, a regexp-based renamer that turns 1C's verbose package filenames (e.g.
   `1c-enterprise83-server_8.3.16-1876_amd64.deb`) into short aliases (`server-8.3.16.1876.deb`).

4. **`http-server/`** — a one-function `net/http.FileServer` wrapper (`server.Run`) exposing the
   download directory; started only when `--enableHttp`/`ONEGET_ENABLE_HTTP_SERVER` is set.

Logging uses `github.com/khorevaa/logos` (a zap-like structured logger), configured via `logos.yaml`
or the `LOGOS_CONFIG` env var (see README "Настройка логов"). Each package that logs declares its own
package-level `log` var scoped by import path (e.g. `"github.com/v8platform/oneget/downloader"`).

## Working in this codebase

- The `RELEASE` argument grammar and per-project filter behavior (Platform/EDT/PostgreSQL/Executor)
  are extensively documented in README.md with worked examples — read it before changing filter or
  argument-parsing logic, since the semantics (e.g. OSX ignoring bitness, `full` only applying to
  `win`) are project-specific and easy to get wrong.
- `downloader/html_test.go` and `downloader/filter_test.go` carry large fixture-driven test tables;
  when releases.1c.ru markup or a filter rule changes, extend these tables rather than writing ad hoc
  tests.
- `unpacker/fixtures/` holds a real sample archive used by `unpacker_test.go`.

## Go developer guidelines

**Logging.** Every package that logs declares its own package-level logger, scoped by full import
path:
```go
var log = logos.New("github.com/v8platform/oneget/<package>").Sugar()
```
Don't pass loggers around or reuse another package's `log` var — declare a new one per package
(see `cmd/get.go`, `downloader/client.go`, `unpacker/unpacker.go`).

**Concurrency.** The established pattern (see `downloader/oneDownloader.go`) is: a buffered channel
of work items, a `sync.WaitGroup` to track completion, and a small buffered channel as a semaphore
(`limit := make(chan struct{}, 10)`) to bound concurrent goroutines. Reuse this pattern for new
concurrent work rather than introducing worker-pool libraries or `errgroup`/`context`-based
cancellation — none of those are used in this codebase.

**Errors.** Accumulate errors from concurrent/multi-item operations with `go.uber.org/multierr`
(`multierr.Append`) instead of failing on the first error; log-and-continue at the point an
individual item fails (`log.Errorf`), and only use `log.Fatalf` for truly unrecoverable startup
errors (see `main.go`'s `handleError`). Don't introduce a different error-wrapping style (e.g.
`fmt.Errorf("%w", ...)` chains or a third-party errors package) without reason — the codebase is
consistently multierr-based.

**Tests.** Tests are table/fixture-driven with `testify` (`downloader/html_test.go`,
`downloader/filter_test.go`, `unpacker/unpacker_test.go` + `unpacker/fixtures/`). When
releases.1c.ru markup or a filter/version rule changes, add a case to the existing table rather
than writing a standalone test function. Run a single test with:
```shell
go test -run TestName ./downloader
```

**Before committing:**
```shell
gofmt -l .        # should print nothing
go vet ./...
go build ./...
go test ./...
go mod tidy       # CI runs this before go test; keep go.mod/go.sum in sync
```
There is no linter config in this repo (no golangci-lint) — don't add one unprompted; the checks
above match what CI actually enforces.
