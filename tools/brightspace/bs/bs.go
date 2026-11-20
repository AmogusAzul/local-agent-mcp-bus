// Package bs reads and submits work in a Brightspace (D2L) site and exposes it
// as MCP tools. The brightspace .exe serves all of them; other programs can pick
// tools and set hooks through Config. See spec/brightspace.md.
package bs

import (
	"context"
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

// Config sets up an API. Only Site is required.
type Config struct {
	Site        string // https://host of the Brightspace site
	DataDir     string // cache, browser profile, updates snapshot (default: os.UserConfigDir()/local-agent-mcp-bus)
	DownloadDir string // parent of each course's default download folder (default: Downloads/brightspace)

	// Approve, when set, runs in every write that has confirm="true", after the
	// preview is built and before anything is sent. An error stops the write and
	// becomes the tool's error.
	Approve func(ctx context.Context, preview string) error
	// CheckSubmit, when set, can refuse a brightspace_submit target before its
	// preview is built.
	CheckSubmit func(SubmitTarget) error
}

// SubmitTarget is what CheckSubmit sees.
type SubmitTarget struct {
	CourseCode, CourseName string
	Folder                 string // assignment folder name
	Group                  string // the user's group for a group folder, else empty
}

// API holds a signed-in session with one site.
type API struct{ a *api }

// New returns an API for cfg. Nothing touches the network until a tool runs.
func New(cfg Config) *API {
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir()
	}
	a := newAPI(newSessionIn(cfg.Site, dir))
	a.jobWait = 30 * time.Second
	a.downloads = cfg.DownloadDir
	a.approve = cfg.Approve
	a.checkSubmit = cfg.CheckSubmit
	return &API{a}
}

// Tools returns every tool, student, staff and generic, by its brightspace_* name.
func (x *API) Tools() []core.Tool { return newTools(x.a) }

// FindBrowser returns the Chromium-based browser the login would use.
func FindBrowser() (string, error) { return findBrowser() }

// approved runs the Approve hook, if any, for a write about to be sent.
func (a *api) approved(ctx context.Context, preview string) error {
	if a.approve == nil {
		return nil
	}
	return a.approve(ctx, preview)
}
