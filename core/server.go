// Package core owns the MCP boilerplate shared by every tool: stdio transport,
// argument schemas, and result/error formatting. Tools only supply logic.
package core

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Arg describes a string argument of a tool.
type Arg struct {
	Name        string
	Description string
	Required    bool
}

// Tool is what a tool author registers.
type Tool struct {
	Name        string
	Description string
	Args        []Arg
	Execute     func(ctx context.Context, args map[string]string) (string, error)
}

// Serve registers the tools and serves MCP over stdio until stdin closes.
func Serve(name, version string, tools ...Tool) {
	log.SetOutput(os.Stderr)
	s := server.NewMCPServer(name, version, server.WithToolCapabilities(false))
	for _, t := range tools {
		s.AddTool(toMCP(t), handler(t))
	}
	if err := server.ServeStdio(s); err != nil {
		log.Printf("%s: %v", name, err)
		os.Exit(1)
	}
}

func toMCP(t Tool) mcp.Tool {
	opts := []mcp.ToolOption{mcp.WithDescription(t.Description)}
	for _, a := range t.Args {
		props := []mcp.PropertyOption{mcp.Description(a.Description)}
		if a.Required {
			props = append(props, mcp.Required())
		}
		opts = append(opts, mcp.WithString(a.Name, props...))
	}
	return mcp.NewTool(t.Name, opts...)
}

func handler(t Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := make(map[string]string, len(t.Args))
		for _, a := range t.Args {
			v := req.GetString(a.Name, "")
			if a.Required && v == "" {
				return mcp.NewToolResultError(fmt.Sprintf("missing required argument %q", a.Name)), nil
			}
			args[a.Name] = v
		}
		out, err := t.Execute(ctx, args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	}
}

// RunCommand runs a process and returns its combined output. A non-zero exit
// becomes an error that includes the output.
func RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %v\n%s", name, err, out)
	}
	return string(out), nil
}
