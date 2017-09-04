## hsync - a filesystem hierarchy synchronizer

`hsync SOURCE TARGET` renames files in TARGET so that identical files found in
SOURCE and TARGET have the same relative path.

The main goal of the program is to make folders synchronization faster by
sparing big file transfers when a simple rename suffices. It complements other
synchronization programs that lack this capability.

See <http://ambrevar.bitbucket.io/hsync> and `hsync -h'`for more details.

## Installation

Set up a Go environment (see <https://golang.org/doc/install>) and run:

	$ go get github.com/ambrevar/hsync

The version number is set at compilation time. To package a specific version,
checkout the corresponding tag and set `version` from the build command, e.g.:

	go build -ldflags "-X main.version=$(git describe --tags --always)"

## Usage

See `hsync -h`.

## License

See LICENSE.
