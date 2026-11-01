// google-tasks reads and changes the user's Google Tasks through the official API.
package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/AmogusAzul/local-agent-mcp-bus/core"
)

const (
	maxTitle = 1024
	maxNotes = 8192
)

var listArg = core.Arg{Name: "list", Description: "List title or id; the default list (usually \"My Tasks\") if omitted"}

func taskArg(what string) core.Arg {
	return core.Arg{Name: "task", Description: "Id or exact title of the task to " + what, Required: true}
}

func (a *api) listsTool(ctx context.Context, args map[string]string) (string, error) {
	switch args["action"] {
	case "":
		lists, err := a.lists(ctx)
		if err != nil {
			return "", err
		}
		def, err := a.defaultList(ctx)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString(today() + "\n")
		for _, l := range lists {
			ts, err := a.tasks(ctx, l.ID, false)
			if err != nil {
				return "", err
			}
			mark := ""
			if l.ID == def.ID {
				mark = " (default)"
			}
			fmt.Fprintf(&b, "%s%s · %d open · id:%s\n", l.Title, mark, len(ts), l.ID)
		}
		return b.String(), nil
	case "create":
		title, err := checkTitle(args["title"])
		if err != nil {
			return "", err
		}
		var l taskList
		if err := a.do(ctx, "POST", "/users/@me/lists", nil, map[string]any{"title": title}, &l); err != nil {
			return "", err
		}
		return fmt.Sprintf("Created list %q (id:%s).", l.Title, l.ID), nil
	case "rename", "delete", "clear":
	default:
		return "", invalid("action must be create, rename, delete or clear, got %q", args["action"])
	}
	if args["list"] == "" {
		return "", invalid("action %s needs list", args["action"])
	}
	l, err := a.resolveList(ctx, args["list"])
	if err != nil {
		return "", err
	}
	switch args["action"] {
	case "rename":
		title, err := checkTitle(args["title"])
		if err != nil {
			return "", err
		}
		if err := a.do(ctx, "PATCH", listsPath(l.ID), nil, map[string]any{"title": title}, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("Renamed list %q to %q.", l.Title, title), nil
	case "delete":
		def, err := a.defaultList(ctx)
		if err != nil {
			return "", err
		}
		if l.ID == def.ID {
			return "", fail("NOT_ALLOWED", "", "Google doesn't allow deleting the default list %q", l.Title)
		}
		if err := a.do(ctx, "DELETE", listsPath(l.ID), nil, nil, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted list %q and all its tasks.", l.Title), nil
	}
	if err := a.do(ctx, "POST", clearPath(l.ID), nil, nil, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("Cleared completed tasks from %q.", l.Title), nil
}

func (a *api) listTool(ctx context.Context, args map[string]string) (string, error) {
	completed, err := flag("completed", args["completed"])
	if err != nil {
		return "", err
	}
	before, err := parseDay("due_before", args["due_before"])
	if err != nil {
		return "", err
	}
	after, err := parseDay("due_after", args["due_after"])
	if err != nil {
		return "", err
	}
	limit := 50
	if s := args["limit"]; s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 {
			return "", invalid("limit must be a positive number, got %q", s)
		}
	}
	l, err := a.resolveList(ctx, args["list"])
	if err != nil {
		return "", err
	}
	all, err := a.tasks(ctx, l.ID, completed)
	if err != nil {
		return "", err
	}
	query := strings.ToLower(args["query"])
	var shown []*task
	open := 0
	for i := range all {
		t := &all[i]
		d := dueDate(t.Due)
		if (before != "" || after != "") && t.Due == "" ||
			before != "" && d > before || after != "" && d < after ||
			query != "" && !strings.Contains(strings.ToLower(t.Title+"\n"+t.Notes), query) {
			continue
		}
		shown = append(shown, t)
		if !t.done() {
			open++
		}
	}
	head := fmt.Sprintf("%s · %s · %d open", today(), l.Title, open)
	if len(shown) == 0 {
		return head + "\nNo matching tasks.", nil
	}
	return head + "\n" + tree(shown, all, limit), nil
}

func (a *api) getTool(ctx context.Context, args map[string]string) (string, error) {
	l, all, err := a.lookup(ctx, args["list"])
	if err != nil {
		return "", err
	}
	t, err := findTask(all, args["task"], l.Title)
	if err != nil {
		return "", err
	}
	return detail(t, all, l.Title), nil
}

func (a *api) createTool(ctx context.Context, args map[string]string) (string, error) {
	title, err := checkTitle(args["title"])
	if err != nil {
		return "", err
	}
	body := map[string]any{"title": title}
	if n := args["notes"]; n != "" {
		if err := checkNotes(n); err != nil {
			return "", err
		}
		body["notes"] = n
	}
	if d := args["due"]; d != "" {
		due, err := parseDue(d)
		if err != nil {
			return "", err
		}
		body["due"] = due
	}
	l, all, err := a.lookup(ctx, args["list"])
	if err != nil {
		return "", err
	}
	q := url.Values{}
	var parent *task
	if p := args["parent"]; p != "" {
		if parent, err = findTask(all, p, l.Title); err != nil {
			return "", err
		}
		if err := canParent(parent, all); err != nil {
			return "", err
		}
		q.Set("parent", parent.ID)
	}
	if s := args["after"]; s != "" {
		prev, err := findTask(all, s, l.Title)
		if err != nil {
			return "", err
		}
		if err := sibling(prev, parent); err != nil {
			return "", err
		}
		q.Set("previous", prev.ID)
	}
	var t task
	if err := a.do(ctx, "POST", tasksPath(l.ID), q, body, &t); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Created %q (id:%s) in %s", t.Title, t.ID, l.Title)
	if parent != nil {
		msg += fmt.Sprintf(" under %q", parent.Title)
	}
	if t.Due != "" {
		msg += ", due " + dueDate(t.Due)
	}
	return msg + ".", nil
}

func (a *api) updateTool(ctx context.Context, args map[string]string) (string, error) {
	title, notes, due, status := args["title"], args["notes"], args["due"], args["status"]
	if title == "" && notes == "" && due == "" && status == "" {
		return "", invalid("pass at least one of title, notes, due, status")
	}
	body := map[string]any{}
	if title != "" {
		var err error
		if title, err = checkTitle(title); err != nil {
			return "", err
		}
		body["title"] = title
	}
	if notes == "none" {
		body["notes"] = nil
	} else if notes != "" {
		if err := checkNotes(notes); err != nil {
			return "", err
		}
		body["notes"] = notes
	}
	if due == "none" {
		body["due"] = nil
	} else if due != "" {
		d, err := parseDue(due)
		if err != nil {
			return "", err
		}
		body["due"] = d
	}
	switch status {
	case "":
	case "done":
		body["status"] = "completed"
	case "open":
		body["status"] = "needsAction"
		body["completed"] = nil
	default:
		return "", invalid("status must be done or open, got %q", status)
	}
	l, all, err := a.lookup(ctx, args["list"])
	if err != nil {
		return "", err
	}
	t, err := findTask(all, args["task"], l.Title)
	if err != nil {
		return "", err
	}
	if _, ok := body["notes"]; ok && t.assigned() {
		return "", assignedErr(t)
	}
	if err := a.do(ctx, "PATCH", taskPath(l.ID, t.ID), nil, body, nil); err != nil {
		return "", err
	}
	var changes []string
	if title != "" {
		changes = append(changes, fmt.Sprintf("title → %q", title))
	}
	if notes != "" {
		changes = append(changes, pick(notes == "none", "notes cleared", "notes changed"))
	}
	if due != "" {
		changes = append(changes, fmt.Sprintf("due %s → %s", orNone(dueDate(t.Due)), pick(due == "none", "none", due)))
	}
	if status != "" {
		changes = append(changes, fmt.Sprintf("status %s → %s", pick(t.done(), "done", "open"), status))
	}
	msg := fmt.Sprintf("Updated %q: %s.", t.Title, strings.Join(changes, ", "))
	if status == "done" {
		if n := openCount(children(all, t.ID)); n > 0 {
			msg += fmt.Sprintf(" It still has %d open subtask(s); Google doesn't complete them with the parent.", n)
		}
	}
	return msg, nil
}

func (a *api) moveTool(ctx context.Context, args map[string]string) (string, error) {
	parentRef, afterRef, toRef := args["parent"], args["after"], args["to_list"]
	if parentRef == "" && afterRef == "" && toRef == "" {
		return "", invalid("pass at least one of parent, after, to_list")
	}
	src, all, err := a.lookup(ctx, args["list"])
	if err != nil {
		return "", err
	}
	t, err := findTask(all, args["task"], src.Title)
	if err != nil {
		return "", err
	}
	if t.assigned() && (parentRef != "" && parentRef != "none" || toRef != "") {
		return "", assignedErr(t)
	}
	dst, dstTasks := src, all
	q := url.Values{}
	if toRef != "" {
		if dst, err = a.resolveList(ctx, toRef); err != nil {
			return "", err
		}
		if dst.ID != src.ID {
			if dstTasks, err = a.tasks(ctx, dst.ID, true); err != nil {
				return "", err
			}
			q.Set("destinationTasklist", dst.ID)
		}
	}
	// Omitting parent puts the task at the top level, so keep its current
	// parent when only reordering within the same list.
	var parent *task
	switch {
	case parentRef == "none":
	case parentRef != "":
		if parent, err = findTask(dstTasks, parentRef, dst.Title); err != nil {
			return "", err
		}
		if parent.ID == t.ID {
			return "", invalid("a task can't be its own parent")
		}
		if err := canParent(parent, dstTasks); err != nil {
			return "", err
		}
		if len(children(all, t.ID)) > 0 {
			return "", fail("UNSUPPORTED_DEPTH", "move its subtasks out first.", "%q has subtasks, so it can't become a subtask: Google Tasks apps only show one level", t.Title)
		}
	case dst.ID == src.ID && t.Parent != "":
		parent = byID(all, t.Parent)
	}
	if parent != nil {
		q.Set("parent", parent.ID)
	}
	if afterRef != "" && afterRef != "top" {
		prev, err := findTask(dstTasks, afterRef, dst.Title)
		if err != nil {
			return "", err
		}
		if prev.ID == t.ID {
			return "", invalid("a task can't be placed after itself")
		}
		if err := sibling(prev, parent); err != nil {
			return "", err
		}
		q.Set("previous", prev.ID)
	}
	if err := a.do(ctx, "POST", movePath(src.ID, t.ID), q, nil, nil); err != nil {
		return "", err
	}
	var where []string
	if dst.ID != src.ID {
		where = append(where, "to "+dst.Title)
	}
	switch {
	case parent != nil && parentRef != "":
		where = append(where, fmt.Sprintf("under %q", parent.Title))
	case parentRef == "none" || parent == nil && t.Parent != "":
		where = append(where, "to the top level")
	}
	switch afterRef {
	case "":
	case "top":
		where = append(where, "first")
	default:
		where = append(where, fmt.Sprintf("after %q", byID(dstTasks, q.Get("previous")).Title))
	}
	if len(where) == 0 {
		where = append(where, "first")
	}
	return fmt.Sprintf("Moved %q %s.", t.Title, strings.Join(where, ", ")), nil
}

func (a *api) deleteTool(ctx context.Context, args map[string]string) (string, error) {
	confirm, err := flag("confirm", args["confirm"])
	if err != nil {
		return "", err
	}
	l, all, err := a.lookup(ctx, args["list"])
	if err != nil {
		return "", err
	}
	t, err := findTask(all, args["task"], l.Title)
	if err != nil {
		return "", err
	}
	if t.assigned() && !confirm {
		return "", fail("NOT_ALLOWED", "pass confirm=\"true\" to delete it in both places.", "%q was assigned from %s; deleting it also deletes it there", t.Title, surface(t))
	}
	// Delete subtasks explicitly so the result doesn't depend on whether
	// Google cascades the parent's deletion.
	kids := children(all, t.ID)
	for _, c := range kids {
		if err := a.do(ctx, "DELETE", taskPath(l.ID, c.ID), nil, nil, nil); err != nil {
			return "", err
		}
	}
	if err := a.do(ctx, "DELETE", taskPath(l.ID, t.ID), nil, nil, nil); err != nil {
		return "", err
	}
	if len(kids) > 0 {
		return fmt.Sprintf("Deleted %q and %d subtask(s).", t.Title, len(kids)), nil
	}
	return fmt.Sprintf("Deleted %q.", t.Title), nil
}

// canParent checks that p can take subtasks: top level and not assigned.
func canParent(p *task, all []task) error {
	if p.assigned() {
		return assignedErr(p)
	}
	if p.Parent != "" {
		top := byID(all, p.Parent)
		name := ""
		if top != nil {
			name = top.Title
		}
		return fail("UNSUPPORTED_DEPTH", fmt.Sprintf("nest under the top-level task %q instead.", name), "%q is a subtask, and Google Tasks apps only show one level of subtasks", p.Title)
	}
	return nil
}

// sibling checks that prev sits under the same parent the task will have.
func sibling(prev, parent *task) error {
	want := ""
	if parent != nil {
		want = parent.ID
	}
	if prev.Parent != want {
		return invalid("after must be a task at the same level (same parent) as where the task goes; %q isn't", prev.Title)
	}
	return nil
}

func assignedErr(t *task) error {
	return fail("UNSUPPORTED_ASSIGNED", "edit it at its origin: "+t.AssignmentInfo.LinkToTask,
		"%q was assigned from %s; only its title, due date, status and order can change here", t.Title, surface(t))
}

func checkTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", invalid("title can't be empty")
	}
	if utf8.RuneCountInString(s) > maxTitle {
		return "", invalid("title is longer than %d characters", maxTitle)
	}
	return s, nil
}

func checkNotes(s string) error {
	if utf8.RuneCountInString(s) > maxNotes {
		return invalid("notes are longer than %d characters", maxNotes)
	}
	return nil
}

func flag(name, s string) (bool, error) {
	switch s {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, invalid("%s must be \"true\" or \"false\", got %q", name, s)
}

func openCount(ts []*task) int {
	n := 0
	for _, t := range ts {
		if !t.done() {
			n++
		}
	}
	return n
}

func pick(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func tools(a *api) []core.Tool {
	return []core.Tool{
		{
			Name:        "tasks_lists",
			Description: "List your Google Tasks lists (id, open count), or manage one with action: create (title), rename (list, title), delete (list; deletes all its tasks), clear (list; hides its completed tasks).",
			Args: []core.Arg{
				{Name: "action", Description: "create | rename | delete | clear; omit to list"},
				{Name: "list", Description: "List title or id, for rename/delete/clear"},
				{Name: "title", Description: "New list title, for create/rename"},
			},
			Execute: a.listsTool,
		},
		{
			Name:        "tasks_list",
			Description: "Show a list's tasks as a tree (subtasks indented) in app order, starting with today's date. Open tasks only unless completed=\"true\".",
			Args: []core.Arg{
				listArg,
				{Name: "completed", Description: "\"true\" to include completed tasks"},
				{Name: "due_before", Description: "Only tasks due on or before this date (YYYY-MM-DD)"},
				{Name: "due_after", Description: "Only tasks due on or after this date (YYYY-MM-DD)"},
				{Name: "query", Description: "Only tasks whose title or notes contain this text"},
				{Name: "limit", Description: "Max tasks shown, default 50"},
			},
			Execute: a.listTool,
		},
		{
			Name:        "tasks_get",
			Description: "Show everything about one task: full notes, status, due date, parent, subtasks, origin links and a link to open it.",
			Args:        []core.Arg{taskArg("show"), listArg},
			Execute:     a.getTool,
		},
		{
			Name:        "tasks_create",
			Description: "Create a task. Google Tasks stores a due date but no time, and has no reminders, stars or (via the API) repeats. Goes first unless after is given. Returns its id.",
			Args: []core.Arg{
				{Name: "title", Description: "Task title", Required: true},
				listArg,
				{Name: "notes", Description: "Notes"},
				{Name: "due", Description: "Due date, YYYY-MM-DD"},
				{Name: "parent", Description: "Id or title of a top-level task to nest under (one level only)"},
				{Name: "after", Description: "Id or title of the sibling to place it after"},
			},
			Reject:  unsupportedArgs,
			Execute: a.createTool,
		},
		{
			Name:        "tasks_update",
			Description: "Change a task's title, notes, due date or status; only the fields passed change. To move it, use tasks_move.",
			Args: []core.Arg{
				taskArg("change"), listArg,
				{Name: "title", Description: "New title"},
				{Name: "notes", Description: "New notes, or \"none\" to clear"},
				{Name: "due", Description: "New due date YYYY-MM-DD, or \"none\" to clear"},
				{Name: "status", Description: "done | open"},
			},
			Reject:  unsupportedArgs,
			Execute: a.updateTool,
		},
		{
			Name:        "tasks_move",
			Description: "Move a task: nest it under a task (parent), take it to the top level (parent=\"none\"), reorder it (after, or after=\"top\"), or send it to another list (to_list). Its subtasks move with it.",
			Args: []core.Arg{
				taskArg("move"), listArg,
				{Name: "parent", Description: "Id or title of the new parent, or \"none\" for the top level"},
				{Name: "after", Description: "Id or title of the sibling to follow, or \"top\""},
				{Name: "to_list", Description: "Destination list title or id"},
			},
			Execute: a.moveTool,
		},
		{
			Name:        "tasks_delete",
			Description: "Delete a task and its subtasks. A task assigned from Docs or Chat is deleted there too and needs confirm=\"true\".",
			Args: []core.Arg{
				taskArg("delete"), listArg,
				{Name: "confirm", Description: "\"true\" to delete an assigned task"},
			},
			Execute: a.deleteTool,
		},
	}
}

func main() {
	a := &api{auth: newAuth(), base: "https://tasks.googleapis.com/tasks/v1"}
	core.Serve("google-tasks", "0.1.0", tools(a)...)
}
