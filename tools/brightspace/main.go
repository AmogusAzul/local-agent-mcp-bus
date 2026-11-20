// brightspace reads and submits work in a Brightspace (D2L) site. See spec/brightspace.md.
package main

import (
	"github.com/AmogusAzul/local-agent-mcp-bus/core"
	"github.com/AmogusAzul/local-agent-mcp-bus/tools/brightspace/bs"
)

// Set at build time with -ldflags -X (see build.sh).
var siteURL = "https://bloqueneon.uniandes.edu.co"

func main() {
	core.Serve("brightspace", "0.1.0", bs.New(bs.Config{Site: siteURL}).Tools()...)
}
