package bs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func (a *api) toolAnnounce(ctx context.Context, args map[string]string) (string, error) {
	c, err := a.oneStaffCourse(ctx, args["course"])
	if err != nil {
		return "", err
	}
	title, body := strings.TrimSpace(args["title"]), strings.TrimSpace(args["body"])
	if title == "" || body == "" {
		return "", invalid("pass title and body")
	}
	start := a.now()
	if s := strings.TrimSpace(args["start"]); s != "" {
		if start, err = userTime(s); err != nil {
			return "", err
		}
	}
	var ups []upload
	for _, p := range splitList(args["files"]) {
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return "", fail("FILE_REJECTED", "pass full paths of regular files, comma-separated.", "%q isn't a readable, non-empty file", p)
		}
		ups = append(ups, upload{p, st.Size()})
	}
	htmlBody := mdToHTML(body)
	item := map[string]any{
		"Title": title, "Body": map[string]string{"Content": htmlBody, "Type": "Html"},
		"StartDate": apiTime(start), "EndDate": nil, "IsGlobal": false, "IsPublished": true,
		"ShowOnlyInCourseOfferings": false, "IsAuthorInfoShown": true,
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Announcement in %s\nTitle: %s\n", c, title)
	if start.After(a.now().Add(time.Minute)) {
		fmt.Fprintf(&b, "Shows from: %s\n", when(start))
	} else {
		b.WriteString("Shows: now (students may get a notification)\n")
	}
	fmt.Fprintf(&b, "Body as students see it:\n%s\n", trunc(plain(htmlBody), 1500))
	for _, u := range ups {
		fmt.Fprintf(&b, "Attachment: %s (%s)\n", u.path, size(u.size))
	}
	if args["confirm"] != "true" {
		b.WriteString("Delete or edit it later on the site." + notSent)
		return b.String(), nil
	}
	if err := a.approved(ctx, b.String()); err != nil {
		return "", err
	}

	path, err := a.lePath(ctx, fmt.Sprintf("/%d/news/", c.ID))
	if err != nil {
		return "", err
	}
	js, _ := json.Marshal(item)
	resp, err := a.send(ctx, func(string) (*http.Request, error) {
		var rd io.Reader
		ctype := "application/json"
		if len(ups) > 0 {
			rd, ctype = mixedBody(js, ups)
		} else {
			rd = strings.NewReader(string(js))
		}
		req, err := http.NewRequest("POST", a.site+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", ctype)
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", statusErr(resp, "The announcement")
	}
	var made struct {
		ID          int64 `json:"Id"`
		Title       string
		StartDate   string
		IsPublished bool
		Attachments []fileRef
	}
	if json.NewDecoder(resp.Body).Decode(&made) != nil || made.ID == 0 {
		return fmt.Sprintf("Posted %q in %s (the site accepted it, but its reply couldn't be read).", title, c.Code), nil
	}
	return fmt.Sprintf("Posted %q in %s: news item %d, shows from %s, %d attachment(s). Edit or delete it on the site (news item %d).",
		made.Title, c.Code, made.ID, when(parseTime(made.StartDate)), len(made.Attachments), made.ID), nil
}
