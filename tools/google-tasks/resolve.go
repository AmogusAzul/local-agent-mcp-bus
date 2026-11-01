package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// resolveList finds a list by id or case-insensitive title; "" is the default list.
func (a *api) resolveList(ctx context.Context, ref string) (taskList, error) {
	if ref == "" {
		return a.defaultList(ctx)
	}
	lists, err := a.lists(ctx)
	if err != nil {
		return taskList{}, err
	}
	var matches []taskList
	for _, l := range lists {
		if l.ID == ref {
			return l, nil
		}
		if strings.EqualFold(l.Title, ref) {
			matches = append(matches, l)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		names := make([]string, len(lists))
		for i, l := range lists {
			names[i] = l.Title
		}
		return taskList{}, fail("LIST_NOT_FOUND", "", "No list named %q. Lists: %s", ref, strings.Join(names, ", "))
	}
	var c []string
	for _, l := range matches {
		c = append(c, "id:"+l.ID)
	}
	return taskList{}, fail("AMBIGUOUS", "pass the list id.", "%d lists named %q: %s", len(matches), ref, strings.Join(c, ", "))
}

// findTask picks a task by id or case-insensitive exact title. Open tasks win
// over completed ones with the same title, so old completed copies of a
// recurring chore don't make every lookup ambiguous.
func findTask(tasks []task, ref, listTitle string) (*task, error) {
	var open, closed []*task
	for i := range tasks {
		t := &tasks[i]
		if t.ID == ref {
			return t, nil
		}
		if strings.EqualFold(strings.TrimSpace(t.Title), strings.TrimSpace(ref)) {
			if t.done() {
				closed = append(closed, t)
			} else {
				open = append(open, t)
			}
		}
	}
	matches := open
	if len(matches) == 0 {
		matches = closed
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fail("TASK_NOT_FOUND", "check the title with tasks_list, or pass the list it's in.", "No task %q in %s", ref, listTitle)
	}
	var c []string
	for _, t := range matches {
		d := "no date"
		if t.Due != "" {
			d = "due " + dueDate(t.Due)
		}
		c = append(c, fmt.Sprintf("id:%s (%s)", t.ID, d))
	}
	return nil, fail("AMBIGUOUS", "pass the id.", "%d tasks named %q: %s", len(matches), ref, strings.Join(c, ", "))
}

// lookup resolves a list and loads all its tasks (completed and hidden too).
func (a *api) lookup(ctx context.Context, listRef string) (taskList, []task, error) {
	l, err := a.resolveList(ctx, listRef)
	if err != nil {
		return l, nil, err
	}
	ts, err := a.tasks(ctx, l.ID, true)
	return l, ts, err
}

func children(tasks []task, id string) []*task {
	var out []*task
	for i := range tasks {
		if tasks[i].Parent == id {
			out = append(out, &tasks[i])
		}
	}
	return out
}

func byID(tasks []task, id string) *task {
	for i := range tasks {
		if tasks[i].ID == id {
			return &tasks[i]
		}
	}
	return nil
}

var (
	dateOnly  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timeHints = regexp.MustCompile(`(?i)\d{1,2}:\d{2}|\d\s*(am|pm)\b|T\d{2}`)
)

// parseDue validates a YYYY-MM-DD date and returns it in the API's RFC 3339 form.
func parseDue(s string) (string, error) {
	s = strings.TrimSpace(s)
	if dateOnly.MatchString(s) {
		if _, err := time.Parse(time.DateOnly, s); err == nil {
			return s + "T00:00:00.000Z", nil
		}
	}
	if timeHints.MatchString(s) {
		return "", fail("UNSUPPORTED_DUE_TIME", "pass due as YYYY-MM-DD and mention the time in the title or notes if needed.", "Google Tasks can only store a due date, not a time (got %q)", s)
	}
	return "", invalid("due must be a date like 2026-11-03, got %q", s)
}

// parseDay validates a YYYY-MM-DD filter bound.
func parseDay(name, s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if _, err := time.Parse(time.DateOnly, s); err != nil || !dateOnly.MatchString(s) {
		return "", invalid("%s must be a date like 2026-11-03, got %q", name, s)
	}
	return s, nil
}

func dueDate(due string) string {
	if len(due) >= 10 {
		return due[:10]
	}
	return due
}
