package tracker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalResolveFindsTicketAcrossFeatureDirectories(t *testing.T) {
	root := t.TempDir()
	writeTicketFile(t, root, "feature-a", "07", "Earlier ticket", "todo")
	writeTicketFile(t, root, "feature-b", "08", "Ticket from feature B", "doing")

	local, err := NewLocal(root, "feature-b")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}

	ticket, err := local.Resolve(context.Background(), "07")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if ticket.Number != 7 || ticket.Title != "Earlier ticket" || ticket.Status != "todo" {
		t.Fatalf("Resolve() = %#v, want ticket 7 with title and status", ticket)
	}
	if ticket.Body == "" {
		t.Fatal("Resolve() returned an empty body")
	}
}

func TestLocalCopyTicketToPreservesRelativePath(t *testing.T) {
	sourceRoot := t.TempDir()
	writeTicketFile(t, sourceRoot, "feature-a", "07", "Copy me", "todo")
	local, err := NewLocal(sourceRoot, "")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}
	destinationRoot := t.TempDir()

	if err := local.CopyTicketTo(context.Background(), 7, destinationRoot); err != nil {
		t.Fatalf("CopyTicketTo() error = %v", err)
	}
	path := filepath.Join(destinationRoot, ".scratch", "feature-a", "issues", "07-ticket.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("copied ticket: %v", err)
	}
	if !strings.Contains(string(contents), "# 07 — Copy me") || !strings.Contains(string(contents), "**Status:** todo") {
		t.Fatalf("copied ticket = %q, want original contents", contents)
	}
}

func TestParseReferenceAcceptsBareAndHashPrefixedNumbers(t *testing.T) {
	tests := []struct {
		name      string
		reference string
		want      int
	}{
		{name: "bare number", reference: "321", want: 321},
		{name: "hash-prefixed number", reference: "#30", want: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseReference(tt.reference)
			if err != nil {
				t.Fatalf("parseReference(%q) error = %v", tt.reference, err)
			}
			if got != tt.want {
				t.Fatalf("parseReference(%q) = %d, want %d", tt.reference, got, tt.want)
			}
		})
	}
}

func TestParseReferenceRejectsInvalidReference(t *testing.T) {
	_, err := parseReference("not-a-ticket")
	if err == nil || !strings.Contains(err.Error(), "want N, #N, or an issue URL") {
		t.Fatalf("parseReference() error = %v, want invalid-reference guidance", err)
	}
}

func TestLocalResolveRejectsAmbiguousTicketNumberWithBothPaths(t *testing.T) {
	root := t.TempDir()
	writeTicketFile(t, root, "feature-a", "09", "First copy", "todo")
	writeTicketFile(t, root, "feature-b", "09", "Second copy", "todo")
	local, err := NewLocal(root, "feature-a")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}

	_, err = local.Resolve(context.Background(), "#09")
	if err == nil {
		t.Fatal("Resolve() error = nil, want duplicate-number error")
	}
	for _, feature := range []string{"feature-a", "feature-b"} {
		if !strings.Contains(err.Error(), filepath.Join(root, ".scratch", feature, "issues", "09-ticket.md")) {
			t.Fatalf("Resolve() error = %q, want path for %s", err, feature)
		}
	}
}

func TestLocalResolveMissingTicketReturnsClearError(t *testing.T) {
	local, err := NewLocal(t.TempDir(), "feature-a")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}

	_, err = local.Resolve(context.Background(), "#42")
	if err == nil || !strings.Contains(err.Error(), "local ticket #42 not found") {
		t.Fatalf("Resolve() error = %v, want clear not-found error", err)
	}
}

func TestLocalCreateUsesContinuousNumberingAcrossFeatureDirectories(t *testing.T) {
	root := t.TempDir()
	writeTicketFile(t, root, "feature-a", "07", "Earlier ticket", "todo")
	local, err := NewLocal(root, "feature-b")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}

	ticket, err := local.Create(context.Background(), "Created ticket", "A newly created ticket body.")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if ticket.Number != 8 || ticket.Title != "Created ticket" || ticket.Status != "todo" {
		t.Fatalf("Create() = %#v, want ticket 8 with todo status", ticket)
	}

	path := filepath.Join(root, ".scratch", "feature-b", "issues", "08-created-ticket.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("created ticket file: %v", err)
	}
	if !strings.Contains(string(contents), "**Status:** todo") || !strings.Contains(string(contents), "A newly created ticket body.") {
		t.Fatalf("created ticket = %q, want body and todo status", contents)
	}
}

func TestLocalListReturnsTicketsFromAllFeatureDirectoriesInNumberOrder(t *testing.T) {
	root := t.TempDir()
	writeTicketFile(t, root, "feature-b", "07", "Later ticket", "doing")
	writeTicketFile(t, root, "feature-a", "02", "Earlier ticket", "todo")
	local, err := NewLocal(root, "feature-a")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}

	tickets, err := local.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tickets) != 2 || tickets[0].Number != 2 || tickets[1].Number != 7 {
		t.Fatalf("List() = %#v, want tickets 2 then 7", tickets)
	}
}

func TestLocalUpdateStatusChangesOnlyTheStatusValue(t *testing.T) {
	root := t.TempDir()
	writeTicketFile(t, root, "feature-a", "03", "Status ticket", "todo")
	path := filepath.Join(root, ".scratch", "feature-a", "issues", "03-ticket.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	local, err := NewLocal(root, "feature-a")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}

	if err := local.UpdateStatus(context.Background(), 3, "doing"); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(before, []byte("**Status:** todo"), []byte("**Status:** doing"), 1)
	if !bytes.Equal(after, want) {
		t.Fatalf("updated ticket bytes differ beyond status line:\n before: %q\n after:  %q\n want:   %q", before, after, want)
	}
}

func TestLocalAddCommentAppendsNotesToTheTicket(t *testing.T) {
	root := t.TempDir()
	writeTicketFile(t, root, "feature-a", "04", "Note ticket", "todo")
	path := filepath.Join(root, ".scratch", "feature-a", "issues", "04-ticket.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	local, err := NewLocal(root, "feature-a")
	if err != nil {
		t.Fatalf("NewLocal() error = %v", err)
	}

	if err := local.AddComment(context.Background(), 4, "Remember to cover the edge case."); err != nil {
		t.Fatalf("AddComment() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) || !strings.Contains(string(after), "## Notes\n\n- Remember to cover the edge case.\n") {
		t.Fatalf("ticket after AddComment() = %q, want original bytes plus appended note", after)
	}
}

func writeTicketFile(t *testing.T, root, feature, number, title, status string) {
	t.Helper()
	path := filepath.Join(root, ".scratch", feature, "issues", number+"-ticket.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "# " + number + " — " + title + "\n\n" +
		"**What to build:** Build the requested behavior.\n\n" +
		"**Blocked by:** None — can start immediately.\n\n" +
		"**Status:** " + status + "\n\n" +
		"- [ ] Demonstrate the behavior\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
