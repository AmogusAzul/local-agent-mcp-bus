package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Google Tasks API v1 resources (only the fields this tool uses).
type taskList struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type task struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Notes     string `json:"notes"`
	Status    string `json:"status"`
	Due       string `json:"due"`
	Completed string `json:"completed"`
	Parent    string `json:"parent"`
	Position  string `json:"position"`
	Hidden    bool   `json:"hidden"`
	Links     []struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Link        string `json:"link"`
	} `json:"links"`
	WebViewLink    string `json:"webViewLink"`
	AssignmentInfo *struct {
		LinkToTask  string `json:"linkToTask"`
		SurfaceType string `json:"surfaceType"`
	} `json:"assignmentInfo"`
}

func (t *task) done() bool     { return t.Status == "completed" }
func (t *task) assigned() bool { return t.AssignmentInfo != nil }

type api struct {
	auth *auth
	base string
}

// do sends one request. A revoked token starts a new login and is retried once.
func (a *api) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	err := a.try(ctx, method, path, q, body, out)
	if isInvalidGrant(err) || errors.Is(err, errUnauthorized) {
		a.auth.invalidate()
		err = a.try(ctx, method, path, q, body, out)
	}
	if isInvalidGrant(err) {
		return fail("GOOGLE_ERROR", "retry to log in again.", "Google rejected the saved login")
	}
	return err
}

var errUnauthorized = errors.New("unauthorized")

func (a *api) try(ctx context.Context, method, path string, q url.Values, body, out any) error {
	hc, err := a.auth.client(ctx)
	if err != nil {
		return err
	}
	u := a.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		if isInvalidGrant(err) {
			return err
		}
		return fail("GOOGLE_ERROR", "check the network and retry.", "request to Google failed: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if resp.StatusCode >= 400 {
		return googleError(resp.StatusCode, b)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// googleError maps an API error response to a fixed code.
func googleError(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	msg := e.Error.Message
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	reason := ""
	if len(e.Error.Errors) > 0 {
		reason = e.Error.Errors[0].Reason
	}
	lower := strings.ToLower(msg)
	switch {
	case status == 429 || strings.Contains(reason, "RateLimit") || reason == "quotaExceeded":
		return fail("RATE_LIMITED", "wait a minute and retry.", "Google is rate-limiting requests")
	case status == 412:
		return fail("CONFLICT", "retry.", "The task changed in another app meanwhile")
	case strings.Contains(lower, "recurr") || strings.Contains(lower, "repeat"):
		return fail("UNSUPPORTED_RECURRENCE", "do it in the Google Tasks app.", "Google refused this for a repeating task (%s)", msg)
	case (status == 400 || status == 403) && strings.Contains(lower, "limit"):
		return fail("LIMIT_REACHED", "", "Google's task limit was reached (%s)", msg)
	}
	return fail("GOOGLE_ERROR", "", "HTTP %d: %s", status, msg)
}

func listsPath(id string) string      { return "/users/@me/lists/" + url.PathEscape(id) }
func tasksPath(list string) string    { return "/lists/" + url.PathEscape(list) + "/tasks" }
func taskPath(list, id string) string { return tasksPath(list) + "/" + url.PathEscape(id) }
func movePath(list, id string) string { return taskPath(list, id) + "/move" }
func clearPath(list string) string    { return "/lists/" + url.PathEscape(list) + "/clear" }

func (a *api) lists(ctx context.Context) ([]taskList, error) {
	var all []taskList
	q := url.Values{"maxResults": {"100"}}
	for {
		var page struct {
			Items         []taskList `json:"items"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := a.do(ctx, "GET", "/users/@me/lists", q, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextPageToken == "" {
			return all, nil
		}
		q.Set("pageToken", page.NextPageToken)
	}
}

func (a *api) defaultList(ctx context.Context) (taskList, error) {
	var l taskList
	err := a.do(ctx, "GET", listsPath("@default"), nil, nil, &l)
	return l, err
}

// tasks returns every task of a list, including assigned ones, and completed
// ones (also those completed in the apps, which are hidden) when completed is set.
func (a *api) tasks(ctx context.Context, list string, completed bool) ([]task, error) {
	var all []task
	c := fmt.Sprint(completed)
	q := url.Values{"maxResults": {"100"}, "showCompleted": {c}, "showHidden": {c}, "showAssigned": {"true"}}
	for {
		var page struct {
			Items         []task `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := a.do(ctx, "GET", tasksPath(list), q, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.NextPageToken == "" {
			return all, nil
		}
		q.Set("pageToken", page.NextPageToken)
	}
}
