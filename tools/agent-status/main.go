// agent-status reports the latest Claude or Codex agent turn in a WSL project.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

const maxWait = 50

// locate prints "<mtime> <agent> <file>" for the newest transcript of the
// project, then its last user lines (prompts can be far back in a long turn)
// and its last lines. It prints nothing if there is none.
const locate = `p=$1
d="$HOME/.claude/projects/$(printf %s "$p" | sed 's/[^A-Za-z0-9]/-/g')"
c=$(ls -t "$d"/*.jsonl 2>/dev/null | head -n1)
x=$(find "$HOME/.codex/sessions" -name 'rollout-*.jsonl' -mtime -7 -printf '%T@ %p\n' 2>/dev/null | sort -rn |
	while read -r _ f; do head -n1 "$f" | grep -qF "\"cwd\":\"$p\"" && { echo "$f"; break; }; done)
pick() { [ -n "$2" ] && echo "$(stat -c %Y "$2") $1 $2"; }
r=$({ pick claude "$c"; pick codex "$x"; } | sort -rn | head -n1)
[ -n "$r" ] || exit 0
set -- $r
echo "$1 $2 $3"
grep -E '"type":"user"|"role":"user"' "$3" | grep -vF tool_result | tail -n 20
tail -n 500 "$3"`

type snapshot struct {
	agent, file   string
	mtime         int64
	done          bool
	prompt, reply string
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// texts returns the text blocks of content, which is a string or a block list,
// and whether it held a tool result.
func texts(content json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s, false
	}
	var blocks []block
	json.Unmarshal(content, &blocks)
	var parts []string
	tool := false
	for _, b := range blocks {
		switch b.Type {
		case "text", "input_text", "output_text":
			parts = append(parts, b.Text)
		case "tool_result":
			tool = true
		}
	}
	return strings.Join(parts, "\n"), tool
}

// isPrompt filters out empty text and agent-injected context such as <environment_context>.
func isPrompt(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !strings.HasPrefix(s, "<")
}

func parseClaude(s *snapshot, lines []string) {
	for _, l := range lines {
		var e struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content    json.RawMessage `json:"content"`
				StopReason string          `json:"stop_reason"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		switch e.Type {
		case "user":
			s.done = false
			if text, tool := texts(e.Message.Content); !tool && !e.IsMeta && isPrompt(text) {
				s.prompt = text
			}
		case "assistant":
			s.done = e.Message.StopReason == "end_turn"
			if text, _ := texts(e.Message.Content); strings.TrimSpace(text) != "" {
				s.reply = text
			}
		}
	}
}

func parseCodex(s *snapshot, lines []string) {
	for _, l := range lines {
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				Type             string          `json:"type"`
				Role             string          `json:"role"`
				Content          json.RawMessage `json:"content"`
				LastAgentMessage string          `json:"last_agent_message"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		p := e.Payload
		switch {
		case p.Type == "task_started":
			s.done = false
		case p.Type == "task_complete":
			s.done = true
			if p.LastAgentMessage != "" {
				s.reply = p.LastAgentMessage
			}
		case e.Type == "response_item" && p.Type == "message":
			text, _ := texts(p.Content)
			if p.Role == "user" && isPrompt(text) {
				s.prompt = text
			} else if p.Role == "assistant" && strings.TrimSpace(text) != "" {
				s.reply = text
			}
		}
	}
}

// parse reads the locate output; ok is false when there is no transcript.
func parse(out string) (s snapshot, ok bool) {
	lines := strings.Split(out, "\n")
	head := strings.SplitN(lines[0], " ", 3)
	if len(head) < 3 {
		return s, false
	}
	s.mtime, _ = strconv.ParseInt(head[0], 10, 64)
	s.agent, s.file = head[1], head[2]
	if s.agent == "codex" {
		parseCodex(&s, lines[1:])
	} else {
		parseClaude(&s, lines[1:])
	}
	return s, true
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func (s snapshot) String() string {
	state := "running"
	if s.done {
		state = "done"
	}
	age := time.Since(time.Unix(s.mtime, 0)).Round(time.Second)
	return fmt.Sprintf("%s, %s, updated %s ago\nprompt: %s\nreply:\n%s",
		s.agent, state, age, truncate(s.prompt, 300), truncate(s.reply, 2000))
}

func read(ctx context.Context, project string) (snapshot, bool, error) {
	out, err := core.RunCommand(ctx, "wsl", "-e", "sh", "-c", locate, "_", project)
	if err != nil {
		return snapshot{}, false, err
	}
	s, ok := parse(strings.TrimSpace(out))
	return s, ok, nil
}

func parseWait(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxWait {
		return 0, fmt.Errorf("wait_seconds must be an integer from 0 to %d, got %q", maxWait, v)
	}
	return n, nil
}

func status(ctx context.Context, args map[string]string) (string, error) {
	project := path.Clean(args["project"])
	if !path.IsAbs(project) {
		return "", fmt.Errorf("project must be an absolute path: %s", project)
	}
	wait, err := parseWait(args["wait_seconds"])
	if err != nil {
		return "", err
	}
	start, ok, err := read(ctx, project)
	if err != nil {
		return "", err
	}
	cur := start
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for time.Now().Before(deadline) {
		// A new turn has finished: done now, and not the same finished turn as at the start.
		if ok && cur.done && (!start.done || cur.file != start.file || cur.prompt != start.prompt || cur.reply != start.reply) {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if cur, ok, err = read(ctx, project); err != nil {
			return "", err
		}
	}
	if !ok {
		return fmt.Sprintf("No Claude or Codex transcript for %s yet; a Zed prompt may still be awaiting review.", project), nil
	}
	return cur.String(), nil
}

func main() {
	core.Serve("agent-status", "0.1.0", core.Tool{
		Name:        "agent-status",
		Description: "Report the latest Claude or Codex agent turn in a WSL project (from Zed or Claude Code): state, prompt and final reply.",
		Args: []core.Arg{
			{Name: "project", Description: "Absolute WSL path of the project", Required: true},
			{Name: "wait_seconds", Description: fmt.Sprintf("Wait up to this many seconds (max %d) for the next turn to finish; omit to return at once", maxWait)},
		},
		Execute: status,
	})
}
