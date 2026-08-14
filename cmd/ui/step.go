package ui

import "fmt"

// Step renders the deterministic "[i/total] " prefix used on sequential
// stage lines (init, export, import). i is 1-based.
func Step(i, total int) string {
	return fmt.Sprintf("[%d/%d] ", i, total)
}
