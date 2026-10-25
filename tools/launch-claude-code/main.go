// launch-claude-code opens a Windows Terminal tab in WSL running Claude Code.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

// preflight runs headless before the tab opens: it fails early on a missing
// claude or project, then stores the launch script from stdin in a temp file.
const preflight = `command -v claude >/dev/null || { echo "claude not found on the login PATH" >&2; exit 1; }
[ -d "$1" ] || { echo "project is not a directory: $1" >&2; exit 1; }
f=$(mktemp) && cat > "$f" && echo "$f"`

// quote wraps s in single quotes for bash.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// launchScript deletes itself and replaces the shell with Claude Code.
func launchScript(project, model, args, task string) string {
	cmd := "exec claude"
	if model != "" {
		cmd += " --model " + quote(model)
	}
	if args != "" {
		cmd += " " + args
	}
	if task != "" {
		cmd += " " + quote(task)
	}
	return fmt.Sprintf("rm -f \"$0\"\ncd %s && %s\n", quote(project), cmd)
}

func hasModelFlag(args string) bool {
	for _, f := range strings.Fields(args) {
		if f == "--model" || strings.HasPrefix(f, "--model=") {
			return true
		}
	}
	return false
}

func launch(ctx context.Context, args map[string]string) (string, error) {
	model := args["model"]
	if model != "" && hasModelFlag(args["args"]) {
		return "", fmt.Errorf("model is set and args also contains --model; use only one")
	}

	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	pre := exec.CommandContext(pctx, "wsl", "-e", "bash", "-lc", preflight, "_", args["project"])
	pre.Stdin = strings.NewReader(launchScript(args["project"], model, args["args"], args["task"]))
	pre.Stdout, pre.Stderr = &stdout, &stderr
	if err := pre.Run(); err != nil {
		return "", fmt.Errorf("preflight: %v\n%s", err, stderr.String())
	}
	// The temp file path is the last line; a login profile may print before it.
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	script := strings.TrimSpace(lines[len(lines)-1])

	if _, err := core.RunCommand(ctx, "wt", "-w", "0", "new-tab", "--profile", "Ubuntu",
		"--", "wsl", "-e", "bash", "-l", script); err != nil {
		return "", err
	}
	return fmt.Sprintf("Launched Claude Code in %s", args["project"]), nil
}

func main() {
	core.Serve("launch-claude-code", "0.3.0", core.Tool{
		Name:        "launch-claude-code",
		Description: "Open a new Windows Terminal tab in WSL and start a Claude Code session, optionally with a task and CLI flags.",
		Args: []core.Arg{
			{Name: "project", Description: "Absolute WSL path of the project, e.g. /home/user/myproject", Required: true},
			{Name: "task", Description: "Initial prompt for Claude Code (optional)"},
			{Name: "model", Description: "Model for the session. Omit to use the Claude Code default. Use `opus` for large or open-ended work: orchestration, multi-step features, refactors. Use `sonnet` for a focused, well-defined task. Use `haiku` for trivial mechanical edits. Also accepts a full model ID."},
			{Name: "args", Description: "Raw Claude Code CLI flags placed before the prompt, e.g. \"--permission-mode plan\" or \"--continue\""},
		},
		Execute: launch,
	})
}
