// LiProxy - a web-driven universal proxy platform.
// Copyright (C) 2026 LiProxy Contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// Command lip is the LiProxy CLI. It talks to a running lipd over the local
// REST API for state-changing commands, and reads the filesystem directly for
// purely local ones such as `version`.
//
// Scope note: only `version` is implemented. The remaining subcommands from
// the README command table (start, stop, import, export, provider, ...) arrive
// in later units; unimplemented names are reported as such instead of being
// silently accepted.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/LiStudioorg/liproxy/pkg/version"
)

// exitUsage is the conventional exit code for a CLI misuse.
const exitUsage = 2

// usage is printed for `lip`, `lip help` and `lip --help`.
const usage = `Usage: lip <command> [flags]

LiProxy command line interface.

Commands:
  version    print version, commit and build time
  help       show this message

Run 'lip <command> --help' for command specific flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without os.Exit, so the dispatch table and exit codes can be
// tested directly.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	switch args[0] {
	case "version", "--version", "-v":
		return cmdVersion(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "lip: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
}

// cmdVersion prints the build identity injected at link time.
func cmdVersion(args []string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(stdout, "Usage: lip version\n\nPrint version, commit and build time.\n")
			return 0
		}
		fmt.Fprintf(stderr, "lip version: unexpected argument %q\n", arg)
		return exitUsage
	}
	fmt.Fprintln(stdout, version.String())
	return 0
}
