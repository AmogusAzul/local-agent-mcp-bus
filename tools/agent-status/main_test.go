package main

import (
	"strings"
	"testing"
)

func TestParseClaude(t *testing.T) {
	lines := []string{
		"1700000000 claude /h/.claude/projects/-p/s.jsonl",
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"fix the bug"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking."},{"type":"tool_use"}],"stop_reason":"tool_use"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"caveat"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Fixed it."}],"stop_reason":"end_turn"}}`,
		`{"type":"system","subtype":"turn_duration"}`,
	}
	s, ok := parse(strings.Join(lines, "\n"))
	if !ok || s.agent != "claude" || !s.done || s.prompt != "fix the bug" || s.reply != "Fixed it." {
		t.Fatalf("got %+v", s)
	}
	s, _ = parse(strings.Join(lines[:4], "\n"))
	if s.done || s.reply != "Looking." {
		t.Errorf("pending tool call: got %+v", s)
	}
}

func TestParseCodex(t *testing.T) {
	lines := []string{
		"1700000000 codex /h/.codex/sessions/rollout-x.jsonl",
		`{"type":"event_msg","payload":{"type":"task_started"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"add tests"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Working."}]}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Added tests."}}`,
	}
	s, ok := parse(strings.Join(lines, "\n"))
	if !ok || s.agent != "codex" || !s.done || s.prompt != "add tests" || s.reply != "Added tests." {
		t.Fatalf("got %+v", s)
	}
	s, _ = parse(strings.Join(lines[:5], "\n"))
	if s.done || s.reply != "Working." {
		t.Errorf("running: got %+v", s)
	}
}

func TestParseEmpty(t *testing.T) {
	if _, ok := parse(""); ok {
		t.Error("empty output should mean no transcript")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("ñandú日本", 3); got != "ñan…" {
		t.Errorf("got %q", got)
	}
	if got := truncate(" short ", 10); got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestParseWait(t *testing.T) {
	for v, ok := range map[string]bool{"": true, "0": true, "50": true, "51": false, "-1": false, "x": false} {
		if _, err := parseWait(v); (err == nil) != ok {
			t.Errorf("parseWait(%q) err = %v", v, err)
		}
	}
}
