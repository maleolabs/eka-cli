package cmd

import (
	"strings"
	"testing"
)

// TestNewProjectionDraftPublishableWithoutEdits: scaffolded drafts of
// the projection types (tkt-, ctr-) must carry the exact projection
// header (rule 8) so they publish straight out of `eka new` — the
// template is type-aware (regression: previously tkt- drafts were
// unpublishable until the header was added by hand). Tickets also
// require a container reference at scaffold time (rule 8): the test
// publishes a container first, then scaffolds and publishes the ticket
// against it; a container-less ticket draft is refused at scaffold.
// Containers require a plan reference at scaffold time (the container
// lifecycle: publish locks the depends-on plan), so the test seeds an
// approved plan first.
func TestNewProjectionDraftPublishableWithoutEdits(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	defer w.Close()

	// Seed the referenced plan: a plan draft, published and approved —
	// a container publishes only against an approved plan (protocol §4,
	// the lock happens atomically with the container birth).
	if code, _, errText := runIn([]string{"new", "plan:roadmap-v1", "--dimension", "planning"}); code != 0 {
		t.Fatalf("new plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("publish plan draft: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "plan:roadmap-v1", "approved", "--by", "test-agent"}); code != 0 {
		t.Fatalf("approve plan: exit = %d\nstderr: %s", code, errText)
	}

	// Seed the referenced container (a container draft needs the
	// depends-on plan reference and publishes straight from the
	// scaffold; its publish locks the plan).
	if code, out, errText := runIn([]string{"new", "ctr:wave-1", "--depends-on", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("new ctr: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if code, _, errText := runIn([]string{"publish", "ctr:wave-1"}); code != 0 {
		t.Fatalf("publish scaffolded ctr draft: exit = %d\nstderr: %s", code, errText)
	}

	// A container-less ticket draft is refused at scaffold time.
	if code, _, errText := runIn([]string{"new", "tkt:auth-login"}); code == 0 {
		t.Fatalf("new tkt without a container reference must be refused\nstderr: %s", errText)
	}

	code, out, errText := runIn([]string{"new", "tkt:auth-login", "--derives-from", "ctr:wave-1"})
	if code != 0 {
		t.Fatalf("new tkt: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}

	code, out, errText = runIn([]string{"publish", "tkt:auth-login"})
	if code != 0 {
		t.Fatalf("publish scaffolded tkt draft: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "atrium-api/tkt:auth-login:1") {
		t.Errorf("publish must report the published form, got:\n%s", out)
	}
}

// TestNewContainerDraftPublishableWithoutEdits: ctr- scaffolds the
// projection header too (its body carries the Work Items table). The
// scaffold requires the depends-on plan reference (the container
// lifecycle guard) and the publish locks the approved plan.
func TestNewContainerDraftPublishableWithoutEdits(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	defer w.Close()

	if code, _, errText := runIn([]string{"new", "plan:roadmap-v1", "--dimension", "planning"}); code != 0 {
		t.Fatalf("new plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("publish plan draft: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "plan:roadmap-v1", "approved", "--by", "test-agent"}); code != 0 {
		t.Fatalf("approve plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"new", "ctr:wave-1", "--depends-on", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("new ctr: exit = %d\nstderr: %s", code, errText)
	}
	if code, out, errText := runIn([]string{"publish", "ctr:wave-1"}); code != 0 {
		t.Fatalf("publish scaffolded ctr draft: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
}
