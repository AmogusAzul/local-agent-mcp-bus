package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// api makes authorized calls to the site's Valence API.
type api struct {
	s    *session
	site string
	hc   *http.Client // follows redirects; downloads can take long, so no overall timeout
	now  func() time.Time

	jobs    jobs
	jobWait time.Duration // how long a call waits for a background job

	mu      sync.Mutex
	lp, le  string
	courses []course
	fetched time.Time
}

func newAPI(s *session) *api {
	return &api{s: s, site: s.site, hc: &http.Client{}, now: time.Now}
}

// send runs a request built by build, with a bearer token. A 401 makes the
// session mint again (or log in) and the request is built and sent once more.
func (a *api) send(ctx context.Context, build func(token string) (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		tok, err := a.s.token(ctx)
		if err != nil {
			return nil, err
		}
		req, err := build(tok)
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := a.hc.Do(req)
		if err != nil {
			return nil, fail("API_ERROR", "check the network and retry.", "Couldn't reach the site: %v", err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			a.s.invalidate(tok)
			continue
		}
		return resp, nil
	}
}

// statusErr turns a non-2xx reply into a coded error.
func statusErr(resp *http.Response, what string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	detail := trunc(strings.TrimSpace(plain(string(b))), 200)
	if detail != "" {
		detail = ": " + detail
	}
	switch resp.StatusCode {
	case 404:
		return fail("NOT_FOUND", "", "%s wasn't found%s", what, detail)
	case 401:
		return fail("API_ERROR", "retry to sign in again.", "%s was refused (401)%s", what, detail)
	case 403:
		return fail("API_ERROR", "", "%s isn't allowed for this account (403)%s", what, detail)
	}
	return fail("API_ERROR", "", "%s failed (HTTP %d)%s", what, resp.StatusCode, detail)
}

// getJSON GETs path (starting with /) and decodes the reply into out.
func (a *api) getJSON(ctx context.Context, path string, out any) error {
	resp, err := a.send(ctx, func(string) (*http.Request, error) {
		return http.NewRequest("GET", a.site+path, nil)
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return statusErr(resp, "The request")
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fail("API_ERROR", "", "Unexpected reply from the site: %v", err)
	}
	return nil
}

// versions finds the newest API versions once.
func (a *api) versions(ctx context.Context) (lp, le string, err error) {
	a.mu.Lock()
	lp, le = a.lp, a.le
	a.mu.Unlock()
	if lp != "" {
		return lp, le, nil
	}
	var vs []struct {
		ProductCode   string
		LatestVersion string
	}
	if err := a.getJSON(ctx, "/d2l/api/versions/", &vs); err != nil {
		return "", "", err
	}
	for _, v := range vs {
		switch v.ProductCode {
		case "lp":
			lp = v.LatestVersion
		case "le":
			le = v.LatestVersion
		}
	}
	if lp == "" || le == "" {
		return "", "", fail("API_ERROR", "", "The site didn't list its API versions")
	}
	a.mu.Lock()
	a.lp, a.le = lp, le
	a.mu.Unlock()
	return lp, le, nil
}

func (a *api) lpPath(ctx context.Context, rest string) (string, error) {
	lp, _, err := a.versions(ctx)
	if err != nil {
		return "", err
	}
	return "/d2l/api/lp/" + lp + rest, nil
}

func (a *api) lePath(ctx context.Context, rest string) (string, error) {
	_, le, err := a.versions(ctx)
	if err != nil {
		return "", err
	}
	return "/d2l/api/le/" + le + rest, nil
}

// course is an enrollment in a course offering.
type course struct {
	ID         int64
	Name, Code string
	Current    bool
}

func (c course) String() string {
	if c.Code != "" {
		return c.Code + " " + c.Name
	}
	return c.Name
}

// allCourses lists the user's course offerings, cached for a few minutes.
func (a *api) allCourses(ctx context.Context) ([]course, error) {
	a.mu.Lock()
	if a.courses != nil && a.now().Sub(a.fetched) < 5*time.Minute {
		cs := a.courses
		a.mu.Unlock()
		return cs, nil
	}
	a.mu.Unlock()

	var out []course
	bookmark := ""
	for page := 0; page < 20; page++ {
		path, err := a.lpPath(ctx, "/enrollments/myenrollments/")
		if err != nil {
			return nil, err
		}
		if bookmark != "" {
			path += "?bookmark=" + url.QueryEscape(bookmark)
		}
		var r struct {
			PagingInfo struct {
				Bookmark     string
				HasMoreItems bool
			}
			Items []struct {
				OrgUnit struct {
					ID   int64 `json:"Id"`
					Type struct{ Code string }
					Name string
					Code string
				}
				Access struct {
					IsActive     bool
					CanAccess    bool
					StartDate    string
					EndDate      string
					LastAccessed string
				}
			}
		}
		if err := a.getJSON(ctx, path, &r); err != nil {
			return nil, err
		}
		for _, it := range r.Items {
			if !strings.Contains(strings.ToLower(it.OrgUnit.Type.Code), "course") {
				continue
			}
			now := a.now()
			start, end := parseTime(it.Access.StartDate), parseTime(it.Access.EndDate)
			cur := it.Access.IsActive && it.Access.CanAccess &&
				(end.IsZero() || end.After(now)) && (start.IsZero() || !start.After(now))
			// A course with no end date never expires on its own. Count it as
			// current only if it was opened recently (or never opened yet).
			if cur && end.IsZero() {
				if la := parseTime(it.Access.LastAccessed); !la.IsZero() && now.Sub(la) > 120*24*time.Hour {
					cur = false
				}
			}
			out = append(out, course{ID: it.OrgUnit.ID, Name: it.OrgUnit.Name, Code: it.OrgUnit.Code, Current: cur})
		}
		if !r.PagingInfo.HasMoreItems || r.PagingInfo.Bookmark == "" {
			break
		}
		bookmark = r.PagingInfo.Bookmark
	}
	a.mu.Lock()
	a.courses, a.fetched = out, a.now()
	a.mu.Unlock()
	return out, nil
}

// resolveCourse finds a course by id or by a fragment of its name or code.
func (a *api) resolveCourse(ctx context.Context, arg string) (course, error) {
	cs, err := a.allCourses(ctx)
	if err != nil {
		return course{}, err
	}
	arg = strings.TrimSpace(arg)
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, c := range cs {
			if c.ID == id {
				return c, nil
			}
		}
		return course{ID: id}, nil
	}
	m := matchCourses(cs, arg, true)
	if len(m) == 0 {
		m = matchCourses(cs, arg, false)
	}
	switch len(m) {
	case 0:
		return course{}, fail("NOT_FOUND", "list courses with brightspace_courses (all=true includes past ones).", "No course matches %q", arg)
	case 1:
		return m[0], nil
	}
	var names []string
	for i, c := range m {
		if i == 5 {
			names = append(names, "…")
			break
		}
		names = append(names, fmt.Sprintf("%d %s", c.ID, c))
	}
	return course{}, invalid("%q matches several courses (%s). Use the id", arg, strings.Join(names, "; "))
}

func matchCourses(cs []course, frag string, currentOnly bool) []course {
	f := strings.ToLower(frag)
	var out []course
	for _, c := range cs {
		if currentOnly && !c.Current {
			continue
		}
		if strings.Contains(strings.ToLower(c.Name), f) || strings.Contains(strings.ToLower(c.Code), f) {
			out = append(out, c)
		}
	}
	return out
}

// currentCourses are the courses in progress now.
func (a *api) currentCourses(ctx context.Context) ([]course, error) {
	cs, err := a.allCourses(ctx)
	if err != nil {
		return nil, err
	}
	var out []course
	for _, c := range cs {
		if c.Current {
			out = append(out, c)
		}
	}
	return out, nil
}

// courseOrCurrent resolves arg, or lists the current courses when arg is empty.
func (a *api) courseOrCurrent(ctx context.Context, arg string) ([]course, error) {
	if strings.TrimSpace(arg) == "" {
		return a.currentCourses(ctx)
	}
	c, err := a.resolveCourse(ctx, arg)
	if err != nil {
		return nil, err
	}
	return []course{c}, nil
}
