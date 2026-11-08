// brightspace reads and submits work in a Brightspace (D2L) site. See spec/brightspace.md.
package main

import (
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

// Set at build time with -ldflags -X (see build.sh).
var siteURL = "https://bloqueneon.uniandes.edu.co"

func newTools(a *api) []core.Tool {
	return []core.Tool{
		{
			Name:        "brightspace_courses",
			Description: "List the user's Brightspace courses (id | code | name). Current ones unless all=true.",
			Args:        []core.Arg{{Name: "all", Description: `"true" to include past courses`}},
			Execute:     a.toolCourses,
		},
		{
			Name:        "brightspace_due",
			Description: "Upcoming assignments and quizzes, soonest first, with submission status.",
			Args: []core.Arg{
				{Name: "days", Description: "look ahead this many days (default 14)"},
				{Name: "course", Description: "course id or name fragment (default: all current)"},
			},
			Execute: a.toolDue,
		},
		{
			Name:        "brightspace_grades",
			Description: "Grades for one course, or a one-line summary per current course.",
			Args:        []core.Arg{{Name: "course", Description: "course id or name fragment"}},
			Execute:     a.toolGrades,
		},
		{
			Name:        "brightspace_announcements",
			Description: "Recent course announcements as plain text.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment (default: all current)"},
				{Name: "days", Description: "how far back (default 14)"},
			},
			Execute: a.toolAnnouncements,
		},
		{
			Name:        "brightspace_content",
			Description: "A course's module and topic tree, or one topic's text.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "topic", Description: "topic id or name fragment; omit for the tree"},
			},
			Execute: a.toolContent,
		},
		{
			Name:        "brightspace_download",
			Description: "Save one course file to a folder and return its path.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "topic", Description: "file topic id or name fragment", Required: true},
				{Name: "dest", Description: "destination folder (default: Downloads/brightspace/<course>)"},
			},
			Execute: a.toolDownload,
		},
		{
			Name:        "brightspace_sync",
			Description: "Mirror a course's files into a folder as a background job; call again with job to follow it. Reruns skip unchanged files.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment (to start)"},
				{Name: "dest", Description: "destination folder (default: Downloads/brightspace/<course>)"},
				{Name: "module", Description: "only modules whose path contains this"},
				{Name: "types", Description: "extensions, comma-separated (e.g. pdf,pptx)"},
				{Name: "max_mb", Description: "skip files bigger than this"},
				{Name: "job", Description: "job id to follow"},
			},
			Execute: a.toolSync,
		},
		{
			Name: "brightspace_submit",
			Description: "Submit files and/or text to an assignment. Without confirm=true it only previews and sends nothing: " +
				"show the preview to the user and submit only after they agree. A submission can't be undone.",
			Args: []core.Arg{
				{Name: "course", Description: "course id or name fragment", Required: true},
				{Name: "assignment", Description: "assignment id or name fragment", Required: true},
				{Name: "files", Description: "full local paths, comma-separated"},
				{Name: "text", Description: "online text, or a comment when files are sent"},
				{Name: "confirm", Description: `"true" to actually submit`},
			},
			Execute: a.toolSubmit,
		},
	}
}

func main() {
	s := newSession(siteURL)
	a := newAPI(s)
	a.jobWait = 30 * time.Second
	core.Serve("brightspace", "0.1.0", newTools(a)...)
}
