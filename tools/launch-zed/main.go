// launch-zed opens a WSL project in Zed with a task pre-filled in the Agent Panel.
package main

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

// preflight checks the file, creates the project if missing (printing
// "created") and prints the --wsl target.
const preflight = `case "$1" in /*) ;; *) echo "project must be an absolute path: $1" >&2; exit 1;; esac
[ -z "$2" ] || [ -f "$1/$2" ] || { echo "file not found in project: $2" >&2; exit 1; }
[ -d "$1" ] || { mkdir -p -- "$1" && echo created; } || exit 1
echo "$(id -un)@$WSL_DISTRO_NAME"`

// linePosition matches a path:line[:col] suffix. Zed 1.25.3 on Windows mangles
// WSL paths with a position into a bogus project root, so it is rejected.
var linePosition = regexp.MustCompile(`:\d+(:\d+)?$`)

// settle gives the remote project time to connect; a zed://agent request sent
// earlier opens the thread but drops the prompt.
const settle = 2 * time.Second

// agentURL builds the zed://agent link; Zed parses it as form-urlencoded.
func agentURL(task string) string {
	return "zed://agent?" + url.Values{"prompt": {task}}.Encode()
}

// isProjectTitle reports whether a Zed window title belongs to project name.
func isProjectTitle(title, name string) bool {
	return title == name || strings.HasPrefix(title, name+" - ")
}

func waitForWindow(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for !isProjectTitle(foregroundTitle(), name) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("Zed window for %s did not take focus; file and prompt not sent", name)
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

func launch(ctx context.Context, args map[string]string) (string, error) {
	project, file, task := args["project"], args["file"], args["task"]
	if linePosition.MatchString(file) {
		return "", fmt.Errorf("file cannot carry a :line position; Zed mangles it for WSL projects")
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := core.RunCommand(pctx, "wsl", "-e", "sh", "-c", preflight, "_", project, file)
	if err != nil {
		return "", err
	}
	lines := strings.Fields(out)
	target, created := lines[len(lines)-1], len(lines) > 1

	// -r (hidden in --help) opens the project as a sidebar workspace of the first
	// window that already holds a WSL project; -e is ignored for remote projects.
	if _, err := core.RunCommand(ctx, "zed", "-r", "--wsl", target, project); err != nil {
		return "", err
	}
	opened := "Opened"
	if created {
		opened = "Created and opened"
	}
	if file == "" && task == "" {
		return fmt.Sprintf("%s %s in Zed.", opened, project), nil
	}
	if err := waitForWindow(ctx, path.Base(project)); err != nil {
		return "", err
	}
	time.Sleep(settle)
	if file != "" {
		// -a opens the file in the workspace that holds it; -r would make it its own workspace.
		if _, err := core.RunCommand(ctx, "zed", "-a", "--wsl", target, path.Join(project, file)); err != nil {
			return "", err
		}
	}
	if task == "" {
		return fmt.Sprintf("%s %s in Zed with %s.", opened, project, file), nil
	}
	if _, err := core.RunCommand(ctx, "zed", agentURL(task)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s in Zed; the prompt is pre-filled and the user must review it and press Enter to send it.", opened, project), nil
}

func main() {
	core.Serve("launch-zed", "0.1.0", core.Tool{
		Name:        "launch-zed",
		Description: "Open a WSL project in Zed, optionally with a task pre-filled in the Agent Panel. Zed never auto-sends it: the user reviews and presses Enter.",
		Args: []core.Arg{
			{Name: "project", Description: "Absolute WSL path of the project, e.g. /home/user/myproject; created if missing", Required: true},
			{Name: "file", Description: "File to open, relative to the project, e.g. src/main.go (no :line; Zed cannot jump to lines in WSL projects)"},
			{Name: "task", Description: "Prompt to pre-fill in a new agent thread; omit to just open the project"},
		},
		Execute: launch,
	})
}
