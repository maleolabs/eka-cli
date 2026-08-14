// Command eka is the official EKA CLI: conformance validation and
// repository bootstrapping.
//
// It is a thin wrapper around the cmd package, which owns the Cobra
// command tree, the help/usage text and the exit code contract:
//
//	0  fully compliant (warnings allowed); init completed and validates
//	1  blocking violations present (validate; or init produced a
//	   non-conformant repository)
//	2  usage or internal error (unknown command, invalid path,
//	   unreadable scan root)
//
// Warnings never affect the exit code.
package main

import (
	"os"

	"github.com/maleolabs/eka-cli/cmd"
)

func main() {
	os.Exit(cmd.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
