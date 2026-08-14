package bootstrap

import (
	"github.com/maleolabs/eka-core/conformance"
)

// This file implements Stage 5 of the bootstrap model: Validation. After
// generation the target must satisfy the same conformance rules that `eka
// validate` enforces, so the two commands can never disagree about what an
// EKA repository is.

// Validator runs the conformance engine over a root directory. It is
// injectable so tests can exercise the stage without a real filesystem
// scan.
type Validator func(root string) (*conformance.Report, error)

// RunValidation runs v over root (defaulting to conformance.Validate) and
// returns the report. A report with blocking violations is a normal result,
// not an error: the caller decides the exit code from report.Pass().
func RunValidation(root string, v Validator) (*conformance.Report, error) {
	if v == nil {
		v = conformance.Validate
	}
	return v(root)
}
