// Package core owns the MCP boilerplate shared by every tool: stdio transport,
// argument schemas, and result/error formatting. Tools only supply logic.
package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

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
	// Reject maps undeclared argument names to the error a call carrying them
	// gets, so a request for an unsupported feature fails instead of being dropped.
	Reject  map[string]string
	Execute func(ctx context.Context, args map[string]string) (string, error)
	// Run replaces Execute for tools that also return binary content.
	Run func(ctx context.Context, args map[string]string) (Result, error)
}

// Result is text plus optional binary content.
type Result struct {
	Text  string
	Blobs []Blob
}

// Blob is binary content. Images are sent as image content, anything else as an
// embedded resource.
type Blob struct {
	URI      string
	MIMEType string
	Data     []byte
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
		raw := req.GetArguments()
		for name, msg := range t.Reject {
			if _, ok := raw[name]; ok {
				return mcp.NewToolResultError(msg), nil
			}
		}
		args := make(map[string]string, len(t.Args))
		for _, a := range t.Args {
			v := req.GetString(a.Name, "")
			if a.Required && v == "" {
				return mcp.NewToolResultError(fmt.Sprintf("missing required argument %q", a.Name)), nil
			}
			args[a.Name] = v
		}
		if t.Run == nil {
			out, err := t.Execute(ctx, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		}
		res, err := t.Run(ctx, args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return toResult(res), nil
	}
}

func toResult(r Result) *mcp.CallToolResult {
	out := mcp.NewToolResultText(r.Text)
	for _, b := range r.Blobs {
		data := base64.StdEncoding.EncodeToString(b.Data)
		if strings.HasPrefix(b.MIMEType, "image/") {
			out.Content = append(out.Content, mcp.NewImageContent(data, b.MIMEType))
			continue
		}
		out.Content = append(out.Content, mcp.NewEmbeddedResource(mcp.BlobResourceContents{
			URI: b.URI, MIMEType: b.MIMEType, Blob: data,
		}))
	}
	return out
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
