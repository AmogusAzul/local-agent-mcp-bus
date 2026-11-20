package bs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApproveHookGuardsConfirmedWrites(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	f := filepath.Join(t.TempDir(), "hw.pdf")
	os.WriteFile(f, []byte("12345"), 0o644)
	args := func() map[string]string {
		return map[string]string{"course": "calc", "assignment": "10", "files": f, "confirm": "true"}
	}

	var seen string
	a.approve = func(_ context.Context, preview string) error {
		seen = preview
		return errors.New("APPROVAL_DENIED: cancelled")
	}
	if _, err := a.toolSubmit(context.Background(), args()); err == nil || !strings.Contains(err.Error(), "APPROVAL_DENIED") {
		t.Fatalf("denied approval: %v", err)
	}
	if len(site.posts) != 0 {
		t.Fatal("sent without approval")
	}
	if !strings.Contains(seen, "hw.pdf") || !strings.Contains(seen, "Assignment:") {
		t.Fatalf("approval saw: %q", seen)
	}

	// A preview never asks.
	seen = ""
	p := args()
	delete(p, "confirm")
	if _, err := a.toolSubmit(context.Background(), p); err != nil || seen != "" {
		t.Fatalf("preview asked for approval: %v %q", err, seen)
	}

	a.approve = func(context.Context, string) error { return nil }
	if _, err := a.toolSubmit(context.Background(), args()); err != nil || len(site.posts) != 1 {
		t.Fatalf("approved submit: %v, posts %d", err, len(site.posts))
	}
}

func TestCheckSubmitHookRefuses(t *testing.T) {
	a, site, _ := newTestAPI(t, true)
	f := filepath.Join(t.TempDir(), "hw.pdf")
	os.WriteFile(f, []byte("12345"), 0o644)
	var got SubmitTarget
	a.checkSubmit = func(t SubmitTarget) error {
		got = t
		return errors.New("REFUSED: no")
	}
	_, err := a.toolSubmit(context.Background(), map[string]string{"course": "calc", "assignment": "10", "files": f})
	if err == nil || !strings.Contains(err.Error(), "REFUSED") {
		t.Fatalf("want refusal, got %v", err)
	}
	if got.Folder == "" || got.CourseCode == "" || got.Group != "" {
		t.Fatalf("target %+v", got)
	}
	if len(site.posts) != 0 {
		t.Fatal("sent")
	}
}

func TestConfigDirs(t *testing.T) {
	data, dl := t.TempDir(), t.TempDir()
	x := New(Config{Site: "https://x", DataDir: data, DownloadDir: dl})
	if x.a.s.path != filepath.Join(data, "brightspace.json") {
		t.Fatalf("cache at %s", x.a.s.path)
	}
	if got := x.a.defaultDest(course{ID: 5, Code: "C 1"}); filepath.Dir(got) != dl {
		t.Fatalf("download dest %s", got)
	}
	if len(x.Tools()) == 0 {
		t.Fatal("no tools")
	}
}
