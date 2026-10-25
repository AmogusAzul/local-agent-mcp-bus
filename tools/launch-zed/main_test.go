package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestAgentURLRoundTrips(t *testing.T) {
	task := "fix the \"login\" bug; it's 100% $HOME's & #1 fault + more\nsecond line: ñandú 日本"
	u := agentURL(task)
	query, ok := strings.CutPrefix(u, "zed://agent?")
	if !ok {
		t.Fatalf("unexpected prefix: %s", u)
	}
	if strings.ContainsAny(query, " \n\"'#") {
		t.Errorf("unencoded characters in %q", query)
	}
	got, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("prompt") != task {
		t.Errorf("got %q, want %q", got.Get("prompt"), task)
	}
}

func TestIsProjectTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"case-atlas": true, "case-atlas - models.py": true,
		"case-atlas-old": false, "other - case-atlas": false, "": false,
	} {
		if got := isProjectTitle(title, "case-atlas"); got != want {
			t.Errorf("isProjectTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

func TestLinePosition(t *testing.T) {
	for file, want := range map[string]bool{
		"src/main.go:12": true, "src/main.go:12:3": true,
		"src/main.go": false, "notes:v2.md": false, "": false,
	} {
		if got := linePosition.MatchString(file); got != want {
			t.Errorf("linePosition(%q) = %v, want %v", file, got, want)
		}
	}
}
