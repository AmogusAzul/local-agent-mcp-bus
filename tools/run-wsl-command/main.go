// run-wsl-command runs a bash command in WSL and returns its output.
package main

import (
	"context"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

func run(ctx context.Context, args map[string]string) (string, error) {
	return core.RunCommand(ctx, "wsl", "-e", "bash", "-c", args["command"])
}

func main() {
	core.Serve("run-wsl-command", "0.1.0", core.Tool{
		Name:        "run-wsl-command",
		Description: "Run a bash command in WSL and return its combined stdout/stderr.",
		Args: []core.Arg{
			{Name: "command", Description: "Bash command string", Required: true},
		},
		Execute: run,
	})
}
