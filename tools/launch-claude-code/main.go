// launch-claude-code opens a Windows Terminal tab in WSL running Claude Code.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

// quote wraps s in single quotes for bash.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func launch(ctx context.Context, args map[string]string) (string, error) {
	cmd := "claude"
	if a := args["args"]; a != "" {
		cmd += " " + a
	}
	if t := args["task"]; t != "" {
		cmd += " " + quote(t)
	}
	script := fmt.Sprintf("cd %s && %s", quote(args["project"]), cmd)
	// wt splits its own command line on ';' unless escaped.
	script = strings.ReplaceAll(script, ";", `\;`)
	if _, err := core.RunCommand(ctx, "wt", "-w", "0", "new-tab", "--profile", "Ubuntu",
		"--", "wsl", "-e", "bash", "-lc", script); err != nil {
		return "", err
	}
	return fmt.Sprintf("Launched Claude Code in %s", args["project"]), nil
}

func main() {
	core.Serve("launch-claude-code", "0.2.0", core.Tool{
		Name:        "launch-claude-code",
		Description: "Open a new Windows Terminal tab in WSL and start a Claude Code session, optionally with a task and CLI flags.",
		Args: []core.Arg{
			{Name: "project", Description: "Absolute WSL path of the project, e.g. /home/user/myproject", Required: true},
			{Name: "task", Description: "Initial prompt for Claude Code (optional)"},
			{Name: "args", Description: "Raw Claude Code CLI flags placed before the prompt, e.g. \"--model opus --permission-mode plan\" or \"--continue\""},
		},
		Execute: launch,
	})
}
