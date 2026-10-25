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
	script := fmt.Sprintf("cd %s && claude %s", quote(args["project"]), quote(args["task"]))
	if _, err := core.RunCommand(ctx, "wt", "-w", "0", "new-tab", "--profile", "Ubuntu",
		"--", "wsl", "-e", "bash", "-c", script); err != nil {
		return "", err
	}
	return fmt.Sprintf("Launched Claude Code in %s", args["project"]), nil
}

func main() {
	core.Serve("launch-claude-code", "0.1.0", core.Tool{
		Name:        "launch-claude-code",
		Description: "Open a new Windows Terminal tab in WSL and start a Claude Code session on a task.",
		Args: []core.Arg{
			{Name: "project", Description: "Absolute WSL path of the project, e.g. /home/user/myproject", Required: true},
			{Name: "task", Description: "Initial prompt for Claude Code", Required: true},
		},
		Execute: launch,
	})
}
