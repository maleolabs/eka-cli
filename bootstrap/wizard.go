package bootstrap

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

// This file implements Stage 3 of the bootstrap model: the Interactive
// Wizard. The wizard is adaptive: it only asks questions whose answers are
// not already known from discovery. When stdin is not a terminal, the
// wizard is skipped entirely and DefaultAnswers provides deterministic
// discovery-derived values.
//
// EKA v1 has no methodology taxonomy, so no methodology question exists:
// there is no canonical set of methodologies to choose from. The question
// is intentionally absent and this fact is documented in the spec.

// QuestionKind identifies one wizard question.
type QuestionKind string

// The wizard questions, asked in this fixed order.
const (
	// QProject asks for the repository project id — the eka.yaml
	// `project` value (ADR-017). Always asked: it is the identity the
	// repository carries at its root.
	QProject QuestionKind = "project"
	// QNamespace asks for the frontmatter namespace — the eka.yaml
	// `namespace` value. Always asked; its default is the answered
	// project id (the two are equal by default and decouple only when
	// the user overrides one).
	QNamespace QuestionKind = "namespace"
	// QGit asks whether to run `git init`. Asked only when the target is
	// not already a git repository and a git executable is available.
	QGit QuestionKind = "git"
	// QAgentsMD asks whether to manage the AGENTS.md workflow-context
	// block. Always asked last (opt-in confirm, default yes); skipped
	// when fixed by flag.
	QAgentsMD QuestionKind = "agents-md"
)

// Question is one wizard prompt: what to ask, how to word it, and the
// default answer offered.
type Question struct {
	Kind    QuestionKind
	Prompt  string
	Default string
}

// Answers are the wizard outcomes consumed by the planner.
type Answers struct {
	// Project is the repository project id (eka.yaml `project`).
	Project string
	// Namespace is the frontmatter namespace (eka.yaml `namespace`).
	Namespace string
	// InitGit requests `git init` in the target.
	InitGit bool
	// AgentsMD requests AGENTS.md workflow-context management
	// (create/merge the marked block, never touching user content).
	AgentsMD bool
	// Interactive reports whether the answers came from an interactive
	// session (affects plan wording for skipped git init).
	Interactive bool
}

// PreAnswers are answers fixed before the wizard runs (flag values in
// bootstrap.Options). A non-empty Project or Namespace fixes that answer:
// the wizard skips the corresponding question entirely. A non-nil
// AgentsMD fixes the agent-context answer the same way.
type PreAnswers struct {
	Project   string
	Namespace string
	AgentsMD  *bool
}

// fallbackName is used when no usable name can be derived from the target
// directory (e.g. bootstrapping the filesystem root).
const fallbackName = "eka-project"

// validIdentPattern is the EKA identifier rule: lowercase letters,
// digits and single hyphens between alphanumeric runs (no leading,
// trailing or double hyphens).
var validIdentPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// IsValidIdent reports whether s is a valid EKA identifier: non-empty,
// lowercase letters/digits, hyphen-separated segments. This is the
// single source of truth for the rule (ADR-017 D1); metadata.ValidIdent
// applies the same pattern — duplicated there by design so the
// metadata package stays self-contained.
func IsValidIdent(s string) bool {
	return validIdentPattern.MatchString(s)
}

// isValidNamespace reports whether s is a valid EKA namespace: the
// identifier rule of IsValidIdent (lowercase letters, digits and single
// hyphens — no '/', ':', whitespace, leading/trailing or double
// hyphens).
func isValidNamespace(s string) bool {
	return IsValidIdent(s)
}

// sanitizeNamespace derives a valid namespace from arbitrary text:
// lowercase, every run of invalid characters becomes a single hyphen,
// leading/trailing hyphens are trimmed. Returns "" when nothing usable
// remains.
func sanitizeNamespace(s string) string {
	var b strings.Builder
	dashPending := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dashPending && b.Len() > 0 {
				b.WriteByte('-')
			}
			dashPending = false
			b.WriteRune(r)
		case r == '-':
			dashPending = true
		default:
			// Whitespace, '/', ':' and everything else act as separators.
			dashPending = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// defaultProject derives the project id default from discovery: the
// sanitized target basename, or the fallback name when nothing usable
// remains.
func defaultProject(d *Discovery) string {
	if IsValidIdent(d.BaseName) {
		return d.BaseName
	}
	if ns := sanitizeNamespace(d.BaseName); ns != "" {
		return ns
	}
	return fallbackName
}

// DefaultAnswers returns the deterministic non-interactive answers derived
// from discovery: the project id and the namespace both default to the
// sanitized target basename (equal BY DEFAULT — they decouple only when
// the user overrides one), git never initialized. Non-interactive runs
// never run `git init`: it is a side effect beyond file writes and would
// break determinism guarantees.
func DefaultAnswers(d *Discovery) Answers {
	p := defaultProject(d)
	return Answers{
		Project:     p,
		Namespace:   p,
		InitGit:     false,
		Interactive: false,
	}
}

// NeededQuestions returns the wizard questions in fixed order: the
// project id, then the namespace, then git init, then the AGENTS.md
// workflow-context confirm. Project and namespace are always asked —
// the project id is the repository identity in eka.yaml and the
// namespace question's default is the answered project id (sequential
// adaptivity applied by Ask). The git question is asked only when the
// target is not already a git repository and a git executable is
// available. The agent-context question is always asked last. The
// function is pure (no I/O) so adaptivity is unit testable.
func NeededQuestions(d *Discovery) []Question {
	qs := []Question{
		{Kind: QProject, Prompt: "Project id", Default: defaultProject(d)},
		{Kind: QNamespace, Prompt: "Namespace (lowercase letters, digits, hyphens)", Default: defaultProject(d)},
	}
	if !d.IsGitRepo && d.GitAvailable {
		qs = append(qs, Question{Kind: QGit, Prompt: "Initialize git repository?", Default: "y"})
	}
	qs = append(qs, Question{Kind: QAgentsMD, Prompt: "Manage AGENTS.md workflow context?", Default: "y"})
	return qs
}

// Ask runs the interactive wizard: prints each needed question to w and
// reads answers from r. Answers are validated as they are entered;
// invalid project ids and namespaces are re-prompted. The wizard is
// sequentially adaptive: the namespace question's default is the
// answered project id. Answers pre-filled through pre (flag values) are
// fixed — the corresponding question is skipped. If the input stream
// ends early (EOF), the remaining answers fall back to their defaults so
// a closed pipe can never hang the run.
func Ask(d *Discovery, r io.Reader, w io.Writer, pre PreAnswers) (Answers, error) {
	a := DefaultAnswers(d)
	a.Interactive = true
	sc := bufio.NewScanner(r)
	for _, q := range NeededQuestions(d) {
		switch q.Kind {
		case QProject:
			if pre.Project != "" {
				a.Project = pre.Project
				continue
			}
			a.Project = askProject(sc, w, a.Project)
		case QNamespace:
			if pre.Namespace != "" {
				a.Namespace = pre.Namespace
				continue
			}
			// Sequential adaptivity: the namespace default is the
			// answered project id.
			a.Namespace = askNamespace(sc, w, a.Project)
		case QGit:
			a.InitGit = askYesNo(sc, w, q, true)
		case QAgentsMD:
			if pre.AgentsMD != nil {
				a.AgentsMD = *pre.AgentsMD
				continue
			}
			a.AgentsMD = askYesNo(sc, w, q, true)
		}
	}
	return a, nil
}

// askProject prompts until a valid project id is entered.
func askProject(sc *bufio.Scanner, w io.Writer, def string) string {
	q := Question{
		Kind:    QProject,
		Prompt:  "Project id",
		Default: def,
	}
	for {
		answer := askLine(sc, w, q, def)
		if IsValidIdent(answer) {
			return answer
		}
		wizInvalid(w, "invalid project id %q — use lowercase letters, digits and hyphens only", answer)
	}
}

// askNamespace prompts until a valid namespace is entered.
func askNamespace(sc *bufio.Scanner, w io.Writer, def string) string {
	q := Question{
		Kind:    QNamespace,
		Prompt:  "Namespace (lowercase letters, digits, hyphens)",
		Default: def,
	}
	for {
		answer := askLine(sc, w, q, def)
		if isValidNamespace(answer) {
			return answer
		}
		wizInvalid(w, "invalid namespace %q — use lowercase letters, digits and hyphens only", answer)
	}
}

// Wizard prompt styling follows the eka-cli visual language with zero
// new dependencies: blank line between questions (section spacing), a
// `>` marker, the label dimmed and the default bright white. Colors render only when w
// is a real terminal — piped output stays byte-deterministic. The input
// echo itself is terminal-controlled and keeps the terminal default.
const (
	wizDim    = "38;5;245"
	wizWhite  = "97"
	wizAccent = "38;5;75"
	wizError  = "38;5;167"
)

// wizColor reports whether prompt colors may be emitted on w.
func wizColor(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// wizPaint wraps text in the SGR code when colors are enabled.
func wizPaint(w io.Writer, code, text string) string {
	if !wizColor(w) {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// wizPrompt renders one prompt line: "> label [default]: " with the
// marker accented, the label dimmed and the default bright white.
func wizPrompt(w io.Writer, label, def string) {
	mark := wizPaint(w, wizAccent, ">")
	name := wizPaint(w, wizDim, label)
	suffix := ""
	if def != "" {
		suffix = " " + wizPaint(w, wizWhite, "["+def+"]")
	}
	fmt.Fprintf(w, "\n%s %s%s: ", mark, name, suffix)
}

// wizInvalid renders a validation failure in error red on TTY.
func wizInvalid(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, wizPaint(w, wizError, format+"\n"), args...)
}

// askLine prints the prompt and reads one line. Empty input or a closed
// stream yields the default.
func askLine(sc *bufio.Scanner, w io.Writer, q Question, def string) string {
	wizPrompt(w, q.Prompt, def)
	if !sc.Scan() {
		fmt.Fprintln(w)
		return def
	}
	fmt.Fprintln(w)
	if answer := strings.TrimSpace(sc.Text()); answer != "" {
		return answer
	}
	return def
}

// askYesNo prints a y/n prompt and reads the answer. Empty input or a
// closed stream yields the default.
func askYesNo(sc *bufio.Scanner, w io.Writer, q Question, def bool) bool {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}
	wizPrompt(w, q.Prompt, suffix)
	if !sc.Scan() {
		fmt.Fprintln(w)
		return def
	}
	fmt.Fprintln(w)
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}
