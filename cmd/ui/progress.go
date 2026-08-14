package ui

import "strings"

// Progress bar thresholds: the bar color follows the completion ratio
// — below progressDanger the bar is danger-colored, at or above
// progressSuccess it is success-colored, in between warning. The
// thresholds are constants so the coloring is deterministic on every
// environment; the percentage text still carries the meaning, color is
// never the sole carrier.
const (
	progressWidth     = 10
	progressDanger    = 34 // percent below which the bar is danger-colored
	progressSuccess   = 67 // percent at which the bar becomes success-colored
	progressBarFilled = "█"
	progressBarEmpty  = "░"
)

// ProgressBar renders a fixed-width completion bar of filled ("█") and
// empty ("░") blocks, colored by the completion ratio (danger < 34%,
// warning 34–66%, success ≥ 67%). With color disabled (non-TTY,
// NO_COLOR, TERM=dumb) the bar is plain text — the percentage shown
// next to it carries the meaning.
//
// A total of zero renders an all-empty bar (no work to complete);
// done is clamped to [0, total].
func ProgressBar(s *Style, done, total int) string {
	if total < 0 {
		total = 0
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	filled := 0
	if total > 0 {
		filled = done * progressWidth / total
	}
	bar := strings.Repeat(progressBarFilled, filled) + strings.Repeat(progressBarEmpty, progressWidth-filled)
	if s.Color {
		percent := 0
		if total > 0 {
			percent = done * 100 / total
		}
		switch {
		case percent < progressDanger:
			bar = s.Error(bar)
		case percent >= progressSuccess:
			bar = s.Success(bar)
		default:
			bar = s.Warning(bar)
		}
	}
	return bar
}
