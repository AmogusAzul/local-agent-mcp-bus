// run-wsl-command runs a bash command in WSL and returns its output.
package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

const defaultTimeout = 60

func run(ctx context.Context, args map[string]string) (string, error) {
	secs := defaultTimeout
	if s := args["timeout_seconds"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("timeout_seconds must be a positive integer, got %q", s)
		}
		secs = n
	}
	// timeout runs inside Linux so the command is really killed; the context is a backstop.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(secs+15)*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wsl", "-e", "timeout", "-k", "5", strconv.Itoa(secs),
		"bash", "-lc", args["command"]).CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 124 {
		return "", fmt.Errorf("timed out after %ds\n%s", secs, out)
	}
	if err != nil {
		return "", fmt.Errorf("wsl: %v\n%s", err, out)
	}
	return string(out), nil
}

func main() {
	core.Serve("run-wsl-command", "0.2.0", core.Tool{
		Name:        "run-wsl-command",
		Description: "Run a bash command in WSL (login shell) and return its combined stdout/stderr.",
		Args: []core.Arg{
			{Name: "command", Description: "Bash command string", Required: true},
			{Name: "timeout_seconds", Description: fmt.Sprintf("Kill the command after this many seconds (default %d)", defaultTimeout)},
		},
		Execute: run,
	})
}
