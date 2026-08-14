package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestProgressBarFilledBlocks(t *testing.T) {
	s, _ := testStyle()
	cases := []struct {
		done, total int
		want        string
	}{
		{0, 10, "░░░░░░░░░░"},
		{1, 10, "█░░░░░░░░░"},
		{5, 10, "█████░░░░░"},
		{10, 10, "██████████"},
		{3, 8, "███░░░░░░░"}, // 37% -> 3 blocks
		{2, 5, "████░░░░░░"}, // 40% -> 4 blocks
		{0, 0, "░░░░░░░░░░"}, // no work -> all empty
	}
	for _, c := range cases {
		if got := ProgressBar(s, c.done, c.total); got != c.want {
			t.Errorf("ProgressBar(%d, %d) = %q, want %q", c.done, c.total, got, c.want)
		}
	}
}

func TestProgressBarClamps(t *testing.T) {
	s, _ := testStyle()
	if got := ProgressBar(s, 12, 10); got != "██████████" {
		t.Errorf("done over total must clamp to full, got %q", got)
	}
	if got := ProgressBar(s, -1, 10); got != "░░░░░░░░░░" {
		t.Errorf("negative done must clamp to empty, got %q", got)
	}
}

func TestProgressBarColors(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{Color: true, W: &buf}
	danger := ProgressBar(s, 1, 10)  // 10%
	warning := ProgressBar(s, 5, 10) // 50%
	success := ProgressBar(s, 8, 10) // 80%
	if !strings.Contains(danger, "\x1b[38;5;167m") {
		t.Errorf("low progress must be danger-colored, got %q", danger)
	}
	if !strings.Contains(warning, "\x1b[38;5;214m") {
		t.Errorf("mid progress must be warning-colored, got %q", warning)
	}
	if !strings.Contains(success, "\x1b[38;5;114m") {
		t.Errorf("high progress must be success-colored, got %q", success)
	}
}

func TestProgressBarNoColor(t *testing.T) {
	s, _ := testStyle() // color disabled
	if got := ProgressBar(s, 5, 10); strings.Contains(got, "\x1b[") {
		t.Errorf("color-off bar must be plain, got %q", got)
	}
}
