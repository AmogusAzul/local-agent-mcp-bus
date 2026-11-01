package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

var now = time.Now

func today() string {
	t := now()
	return fmt.Sprintf("Today: %s (%s)", t.Format(time.DateOnly), t.Format("Mon"))
}

// line renders one task as "[ ] title · due … · id:…", indented by depth.
func line(b *strings.Builder, t *task, indent, note string) {
	box := "[ ]"
	if t.done() {
		box = "[x]"
	}
	fmt.Fprintf(b, "%s%s %s", indent, box, t.Title)
	if t.Due != "" {
		fmt.Fprintf(b, " · due %s", dueDate(t.Due))
	}
	if t.assigned() {
		b.WriteString(" · assigned from " + surface(t))
	}
	if note != "" {
		b.WriteString(" · " + note)
	}
	fmt.Fprintf(b, " · id:%s\n", t.ID)
	if n := firstLine(t.Notes, 80); n != "" {
		fmt.Fprintf(b, "%s    notes: %s\n", indent, n)
	}
}

func surface(t *task) string {
	switch t.AssignmentInfo.SurfaceType {
	case "DOCUMENT":
		return "Docs"
	case "SPACE":
		return "Chat"
	case "GMAIL":
		return "Gmail"
	}
	return "another app"
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	cut := false
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s, cut = strings.TrimSpace(s[:i]), true
	}
	if r := []rune(s); len(r) > max {
		s, cut = string(r[:max]), true
	}
	if cut {
		s += "…"
	}
	return s
}

func sortByPosition(ts []*task) {
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Position < ts[j].Position })
}

// tree renders tasks as the app shows them: top-level tasks by position, each
// followed by its subtasks. A subtask whose parent was filtered out is shown at
// the top level with a note. At most limit tasks are printed.
func tree(tasks []*task, all []task, limit int) string {
	in := map[string]bool{}
	for _, t := range tasks {
		in[t.ID] = true
	}
	var top []*task
	kids := map[string][]*task{}
	for _, t := range tasks {
		if t.Parent != "" && in[t.Parent] {
			kids[t.Parent] = append(kids[t.Parent], t)
		} else {
			top = append(top, t)
		}
	}
	sortByPosition(top)
	var b strings.Builder
	n := 0
	emit := func(t *task, indent, note string) bool {
		if n == limit {
			return false
		}
		line(&b, t, indent, note)
		n++
		return true
	}
	for _, t := range top {
		note := ""
		if t.Parent != "" {
			if p := byID(all, t.Parent); p != nil {
				note = fmt.Sprintf("subtask of %q", p.Title)
			}
		}
		if !emit(t, "", note) {
			break
		}
		k := kids[t.ID]
		sortByPosition(k)
		for _, c := range k {
			if !emit(c, "    ", "") {
				break
			}
		}
	}
	if rest := len(tasks) - n; rest > 0 {
		fmt.Fprintf(&b, "… %d more (raise limit or filter)\n", rest)
	}
	return b.String()
}

// detail renders everything about one task for tasks_get.
func detail(t *task, all []task, listTitle string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nid: %s\nlist: %s\n", t.Title, t.ID, listTitle)
	if t.done() {
		fmt.Fprintf(&b, "status: done %s\n", dueDate(t.Completed))
	} else {
		b.WriteString("status: open\n")
	}
	if t.Due != "" {
		fmt.Fprintf(&b, "due: %s\n", dueDate(t.Due))
	}
	if t.Parent != "" {
		if p := byID(all, t.Parent); p != nil {
			fmt.Fprintf(&b, "parent: %s (id:%s)\n", p.Title, p.ID)
		}
	}
	if k := children(all, t.ID); len(k) > 0 {
		sortByPosition(k)
		b.WriteString("subtasks:\n")
		for _, c := range k {
			line(&b, c, "  ", "")
		}
	}
	if t.assigned() {
		fmt.Fprintf(&b, "assigned from %s: %s\n", surface(t), t.AssignmentInfo.LinkToTask)
	}
	for _, l := range t.Links {
		d := l.Description
		if d == "" {
			d = l.Type
		}
		fmt.Fprintf(&b, "link (%s): %s\n", d, l.Link)
	}
	if t.WebViewLink != "" {
		fmt.Fprintf(&b, "open: %s\n", t.WebViewLink)
	}
	if t.Notes != "" {
		fmt.Fprintf(&b, "notes:\n%s\n", t.Notes)
	}
	return b.String()
}
