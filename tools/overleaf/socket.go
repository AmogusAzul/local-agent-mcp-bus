package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// entity is one doc, file or folder of a project tree, with the id that
// uploads and deletes need.
type entity struct {
	ID   string
	Type string // "doc", "file" or "folder"
}

// tree maps /-separated paths to entities; the root folder is "".
type tree map[string]entity

type folderJSON struct {
	ID      string       `json:"_id"`
	Name    string       `json:"name"`
	Docs    []leafJSON   `json:"docs"`
	Files   []leafJSON   `json:"fileRefs"`
	Folders []folderJSON `json:"folders"`
}

type leafJSON struct {
	ID   string `json:"_id"`
	Name string `json:"name"`
}

func flatten(root folderJSON) tree {
	t := tree{}
	var walk func(f folderJSON, prefix string)
	walk = func(f folderJSON, prefix string) {
		t[strings.TrimSuffix(prefix, "/")] = entity{f.ID, "folder"}
		for _, d := range f.Docs {
			t[prefix+d.Name] = entity{d.ID, "doc"}
		}
		for _, d := range f.Files {
			t[prefix+d.Name] = entity{d.ID, "file"}
		}
		for _, sub := range f.Folders {
			walk(sub, prefix+sub.Name+"/")
		}
	}
	walk(root, "")
	return t
}

// join opens a Socket.IO 0.9 xhr-polling session on the project and returns
// its tree from the joinProjectResponse event.
func (c *client) join(ctx context.Context, id string) (tree, error) {
	q := "?projectId=" + url.QueryEscape(id) + "&t="
	now := func() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }
	b, err := c.do(ctx, "GET", "/socket.io/1/"+q+now(), nil, "", false)
	if err != nil {
		return nil, err
	}
	sid, _, _ := strings.Cut(string(b), ":")
	if sid == "" {
		return nil, fail("OVERLEAF_ERROR", "", "the project socket handshake failed")
	}
	poll := "/socket.io/1/xhr-polling/" + url.PathEscape(sid) + q
	defer c.do(context.WithoutCancel(ctx), "POST", poll+now(), strings.NewReader("0::"), "text/plain;charset=UTF-8", false)
	for range 6 {
		b, err := c.do(ctx, "GET", poll+now(), nil, "", false)
		if err != nil {
			return nil, err
		}
		t, done, err := parseFrames(string(b))
		if done || err != nil {
			return t, err
		}
	}
	return nil, fail("OVERLEAF_ERROR", "retry in a moment.", "Overleaf didn't send the project tree")
}

// parseFrames reads one xhr-polling payload: a single frame or several framed
// as �<len>�<frame>. done is true once the tree (or an error) is found.
func parseFrames(payload string) (tree, bool, error) {
	var frames []string
	if strings.HasPrefix(payload, "�") {
		for _, part := range strings.Split(payload, "�") {
			if part != "" {
				if _, err := strconv.Atoi(part); err != nil {
					frames = append(frames, part)
				}
			}
		}
	} else {
		frames = []string{payload}
	}
	for _, f := range frames {
		switch {
		case strings.HasPrefix(f, "7:"), strings.HasPrefix(f, "0:"):
			return nil, true, fail("OVERLEAF_ERROR", "check that this account can open the project.", "the project socket was refused (%s)", trimFrame(f))
		case strings.HasPrefix(f, "5:"):
			// 5:[id]:[endpoint]:{json}
			parts := strings.SplitN(f, ":", 4)
			if len(parts) < 4 {
				continue
			}
			var ev struct {
				Name string            `json:"name"`
				Args []json.RawMessage `json:"args"`
			}
			if json.Unmarshal([]byte(parts[3]), &ev) != nil {
				continue
			}
			switch ev.Name {
			case "joinProjectResponse":
				var a struct {
					Project struct {
						RootFolder []folderJSON `json:"rootFolder"`
					} `json:"project"`
				}
				if len(ev.Args) == 0 || json.Unmarshal(ev.Args[0], &a) != nil || len(a.Project.RootFolder) == 0 {
					return nil, true, fail("OVERLEAF_ERROR", "", "unexpected project tree")
				}
				return flatten(a.Project.RootFolder[0]), true, nil
			case "connectionRejected":
				return nil, true, fail("OVERLEAF_ERROR", "", "the project socket was rejected: %s", trimFrame(parts[3]))
			}
		}
	}
	return nil, false, nil
}

func trimFrame(s string) string {
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
