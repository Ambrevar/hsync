## hsync - a filesystem hierarchy synchronizer

`hsync SOURCE TARGET` renames files in TARGET so that identical files found in
SOURCE and TARGET have the same relative path.

The main goal of the program is to make folders synchronization faster by
sparing big file transfers when a simple rename suffices. It complements other
synchronization programs that lack this capability.

See <http://ambrevar.bitbucket.org/hsync> and `hsync -h'`for more details.

## Installation

Set up a Go environment (see <https://golang.org/doc/install>) and run:

	$ go get bitbucket.org/ambrevar/hsync

## Usage

See `hsync -h`.

## License

See LICENSE.
