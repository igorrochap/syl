# rig

`rig` sets up and runs an agentic coding workflow across projects, driving
multiple agent CLIs through one command-line tool.

## Installation

Install the latest prebuilt binary on Linux or macOS without installing Go:

```sh
curl -fsSL https://raw.githubusercontent.com/igorrochap/rig/main/scripts/install.sh | sh
```

The installer verifies the release checksum and writes `rig` to
`~/.local/bin` by default. Select another directory or a specific release with:

```sh
curl -fsSL https://raw.githubusercontent.com/igorrochap/rig/main/scripts/install.sh \
  | sh -s -- --dir /usr/local/bin --version v1.2.3
```

Make sure the selected directory is on `PATH`.

To build from source, install Go 1.23 or later and run from the repository root:

```sh
go build -o rig ./cmd/rig
```

You can also run the CLI without keeping a binary:

```sh
go run ./cmd/rig --help
```

## Development

Run the local checks with:

```sh
gofmt -w $(git ls-files '*.go')
go vet ./...
go test ./...
go test -race ./...
scripts/install_test.sh
```

Pull requests run formatting, ShellCheck, module-file, vet, installer, test,
and race-detector checks. Pushes to `main` run those checks and then build
release archives for Linux and macOS on amd64 and arm64.

Successful `main` builds use Conventional Commit messages to create semantic
GitHub releases. Each release contains a checksummed archive for every
supported platform, which the installer consumes.
