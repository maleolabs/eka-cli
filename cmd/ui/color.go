package ui

// ANSI SGR 256-color palette (hand-rolled, no dependency). The palette
// is intentionally soft and low-contrast: these are the only colors the
// CLI may emit. Colors are never the sole carrier of meaning — icons and
// text carry meaning, color is decoration.
const (
	// ColorInfo is the soft blue used for informational labels.
	ColorInfo = "38;5;75"
	// ColorSuccess is the soft green used for completed steps and PASS.
	ColorSuccess = "38;5;114"
	// ColorWarning is the amber used for warnings.
	ColorWarning = "38;5;214"
	// ColorError is the muted red used for failures.
	ColorError = "38;5;167"
	// ColorProgress is the soft cyan used for the active spinner frame.
	ColorProgress = "38;5;80"
	// ColorDim is the gray used for secondary/detail lines.
	ColorDim = "38;5;245"
	// ColorAccent is the heading color. Headings use Info (soft blue);
	// there is deliberately no separate accent hue.
	ColorAccent = ColorInfo
)

// paint wraps text in the given SGR code when colors are enabled;
// otherwise it returns text unchanged. It is the only function in the
// package that emits escape sequences.
func (s *Style) paint(code, text string) string {
	if !s.Color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Info renders informational text (soft blue).
func (s *Style) Info(text string) string { return s.paint(ColorInfo, text) }

// Success renders success text (soft green).
func (s *Style) Success(text string) string { return s.paint(ColorSuccess, text) }

// Warning renders warning text (amber).
func (s *Style) Warning(text string) string { return s.paint(ColorWarning, text) }

// Error renders error text (muted red).
func (s *Style) Error(text string) string { return s.paint(ColorError, text) }

// Progress renders active/loading text (soft cyan).
func (s *Style) Progress(text string) string { return s.paint(ColorProgress, text) }

// Dim renders secondary text (gray).
func (s *Style) Dim(text string) string { return s.paint(ColorDim, text) }

// Accent renders heading text. Headings use Info.
func (s *Style) Accent(text string) string { return s.paint(ColorAccent, text) }
