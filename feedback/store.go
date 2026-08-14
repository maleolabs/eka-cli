package feedback

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ErrNotFound reports that no feedback file exists for the requested
// identity. It is the sentinel the CLI maps to the usage class (exit 2,
// unknown id).
var ErrNotFound = errors.New("feedback not found")

// Store is the feedback directory: Dir = <home>/feedback (ADR-026
// §Decision 1 — home-area storage, repository-independent). Feedback
// lives outside any repository and never enters the canonical store.
type Store struct {
	// Dir is the feedback directory, e.g. <home>/feedback.
	Dir string
}

// New returns the feedback store rooted under home (the EKA workspace
// home, e.g. $EKA_HOME or ~/.eka).
func New(home string) *Store {
	return &Store{Dir: filepath.Join(home, "feedback")}
}

// fileName maps a feedback id to its file name, accepting ids with or
// without the ".md" suffix.
func (s *Store) fileName(id string) string {
	if strings.HasSuffix(id, ".md") {
		return id
	}
	return id + ".md"
}

// Save writes <Dir>/<f.ID>.md. The directory is created with 0700
// permissions when missing (mirroring the workspace security posture:
// the reports are private to the user); the file itself is written
// 0600. A malformed feedback is refused before any write.
func (s *Store) Save(f *Feedback) error {
	if err := f.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("feedback: cannot create %s: %w", s.Dir, err)
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return fmt.Errorf("feedback: cannot secure %s: %w", s.Dir, err)
	}
	data, err := f.Marshal()
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, s.fileName(f.ID))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("feedback: cannot write %s: %w", path, err)
	}
	return nil
}

// Load reads <Dir>/<id>.md. Ids without the ".md" suffix are accepted.
// A missing file returns ErrNotFound. The id is validated before
// joining (it is user input on the publish path — an id carrying path
// separators must never escape the feedback directory).
func (s *Store) Load(id string) (*Feedback, error) {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("invalid feedback id %q", id)
	}
	path := filepath.Join(s.Dir, s.fileName(id))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("feedback: cannot read %s: %w", path, err)
	}
	f, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// List returns all local feedback, sorted by id descending (newest
// first — ids embed YYYYMMDD). Deterministic and honest: the first
// malformed file fails the whole list with an error naming the file —
// a silent skip would hide a broken report.
func (s *Store) List() ([]*Feedback, error) {
	matches, err := filepath.Glob(filepath.Join(s.Dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("feedback: cannot scan %s: %w", s.Dir, err)
	}
	out := make([]*Feedback, 0, len(matches))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("feedback: cannot read %s: %w", path, err)
		}
		f, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("feedback: %s: %w", filepath.Base(path), err)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// NewID derives the next free feedback identity: fbk-<YYYYMMDD>-<slug>.
// The slug is the lowercase title with every non-alphanumeric rune
// collapsed to "-" and edges trimmed; an empty slug falls back to
// "untitled". When the base identity already exists as a file, the
// suffix -2, -3, ... is appended until a free identity is found
// (collisions are possible within one day and one slug).
func (s *Store) NewID(title string, created time.Time) string {
	base := "fbk-" + created.Format("20060102") + "-" + slugify(title)
	if _, err := os.Stat(filepath.Join(s.Dir, s.fileName(base))); os.IsNotExist(err) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, err := os.Stat(filepath.Join(s.Dir, s.fileName(candidate))); os.IsNotExist(err) {
			return candidate
		}
	}
}

// slugify renders the id slug: lowercase, non-alphanumeric runes
// collapsed to a single "-", edges trimmed, empty result -> "untitled".
func slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "untitled"
	}
	return slug
}

// MarkPublished rewrites <id>.md atomically (write <id>.md.tmp then
// os.Rename) with status: published and the issue number + URL written
// by publish. A missing or already-published feedback is refused.
func (s *Store) MarkPublished(id string, issueNumber int, issueURL string) error {
	f, err := s.Load(id)
	if err != nil {
		return err
	}
	if f.Status == StatusPublished {
		return fmt.Errorf("already published as #%d %s", f.IssueNumber, f.IssueURL)
	}
	f.Status = StatusPublished
	f.IssueNumber = issueNumber
	f.IssueURL = issueURL
	data, err := f.Marshal()
	if err != nil {
		return err
	}
	name := s.fileName(id)
	tmp := filepath.Join(s.Dir, name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("feedback: cannot stage %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, name)); err != nil {
		os.Remove(tmp) // debris cleanup; the report itself is untouched
		return fmt.Errorf("feedback: cannot replace %s: %w", filepath.Join(s.Dir, name), err)
	}
	return nil
}
