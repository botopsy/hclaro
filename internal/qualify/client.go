package qualify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client drives Hclaro's HTTP API as the UI does (web/src/api.ts): bearer token, exact Origin
// and the CSRF header on mutations, JSON bodies, SSE read with fetch semantics. It uses only
// routes the UI uses. Every response body and SSE event is appended to a raw transcript under
// the scenario directory for the canary check.
type Client struct {
	h          *Hclaro
	s          *Scenario
	http       *http.Client
	csrf       string
	transcript string
}

func newClient(h *Hclaro, s *Scenario) *Client {
	// No proxy: Hclaro listens on loopback only.
	tr := &http.Transport{Proxy: nil, DisableCompression: true}
	return &Client{h: h, s: s, http: &http.Client{Transport: tr, Timeout: 0},
		transcript: filepath.Join(s.Dir, "responses.raw")}
}

// Response is one API reply.
type Response struct {
	Status int
	Code   string
	Body   []byte
}

func (c *Client) session() error {
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	r, err := c.Do("GET", "session", nil, &body)
	if err != nil {
		return err
	}
	if r.Status != 200 || body.CSRF == "" {
		return fmt.Errorf("GET /api/v1/session: status %d", r.Status)
	}
	c.csrf = body.CSRF
	return nil
}

// Request options for negative checks.
type reqOpts struct {
	noToken, noCSRF bool
	origin          string
}

// Do sends a JSON request to /api/v1/<path> and decodes a 2xx body into out.
func (c *Client) Do(method, path string, body, out any) (Response, error) {
	return c.do(method, path, body, out, reqOpts{})
}

func (c *Client) do(method, path string, body, out any, o reqOpts) (Response, error) {
	var payload io.Reader
	if method != "GET" {
		b, err := json.Marshal(body)
		if err != nil {
			return Response{}, err
		}
		payload = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	u := c.h.baseURL().String() + "/api/v1/" + path
	req, err := http.NewRequestWithContext(ctx, method, u, payload)
	if err != nil {
		return Response{}, err
	}
	if !o.noToken {
		req.Header.Set("Authorization", "Bearer "+c.h.Token)
	}
	if method != "GET" {
		origin := c.h.Origin
		if o.origin != "" {
			origin = o.origin
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		if !o.noCSRF {
			req.Header.Set("X-Hclaro-CSRF", c.csrf)
		}
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("%s /api/v1/%s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return Response{}, err
	}
	c.record(method+" "+path, b)
	r := Response{Status: resp.StatusCode, Body: b}
	var e struct {
		Error string `json:"error"`
	}
	if resp.StatusCode >= 300 && json.Unmarshal(b, &e) == nil {
		r.Code = e.Error
	}
	c.s.step(Step{Route: method + " /api/v1/" + routeOf(path), HTTPStatus: r.Status, Code: r.Code, DurationMs: time.Since(start).Milliseconds()})
	if out != nil && r.Status < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			return r, fmt.Errorf("%s /api/v1/%s: decode: %v", method, path, err)
		}
	}
	return r, nil
}

// routeOf replaces IDs (32+ hex characters) with {id} so steps name routes, not instances.
func routeOf(path string) string {
	path, _, _ = strings.Cut(path, "?")
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if len(p) >= 32 && strings.Trim(p, "0123456789abcdef") == "" {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func (c *Client) record(what string, b []byte) {
	f, err := os.OpenFile(c.transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "### %s\n%s\n", what, b)
}

// Operation is the part of Hclaro's operation record the harness reads.
type Operation struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	PlanID     string `json:"planId"`
	ExitCode   *int   `json:"exitCode"`
	Diagnostic *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"diagnostic"`
}

// Follow reads the operation's SSE stream (path is "operations/<id>" or a scoped
// equivalent) until the server's end event and returns the last status. timeout is hard.
func (c *Client) Follow(path string, timeout time.Duration) (Operation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.h.baseURL().String()+"/api/v1/"+path+"/events?after=0", nil)
	if err != nil {
		return Operation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.h.Token)
	req.Header.Set("Accept", "text/event-stream")
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return Operation{}, err
	}
	defer resp.Body.Close()
	step := Step{Route: "GET /api/v1/" + routeOf(path) + "/events", HTTPStatus: resp.StatusCode}
	defer func() { step.DurationMs = time.Since(start).Milliseconds(); c.s.step(step) }()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		c.record("SSE "+path, b)
		return Operation{}, fmt.Errorf("events: status %d", resp.StatusCode)
	}
	var last Operation
	ended := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	event, data := "message", ""
	for sc.Scan() {
		line := sc.Text()
		c.record("SSE "+path, []byte(line))
		switch {
		case strings.HasPrefix(line, "event: "):
			event = line[7:]
		case strings.HasPrefix(line, "data: "):
			data += line[6:]
		case line == "":
			if event == "status" && data != "" {
				_ = json.Unmarshal([]byte(data), &last)
			}
			if event == "end" {
				ended = true
			}
			event, data = "message", ""
		}
		if ended {
			break
		}
	}
	if !ended {
		if ctx.Err() != nil {
			return last, fmt.Errorf("events: no end event within %s", timeout)
		}
		return last, fmt.Errorf("events: stream closed without an end event: %v", sc.Err())
	}
	return last, nil
}

// NegativeChecks sends one request without the token, one with a wrong Origin and one without
// CSRF, and fails unless each is refused with the guard's documented status and code.
func (c *Client) NegativeChecks() error {
	cases := []struct {
		name, method string
		o            reqOpts
		status       int
		code         string
	}{
		{"no token", "GET", reqOpts{noToken: true}, 401, "unauthorized"},
		{"wrong Origin", "POST", reqOpts{origin: "http://127.0.0.1:1"}, 403, "forbidden_origin"},
		{"no CSRF", "POST", reqOpts{noCSRF: true}, 403, "csrf_invalid"},
	}
	for _, k := range cases {
		path := "session"
		var body any
		if k.method == "POST" {
			path, body = "workspace/reload", map[string]any{}
		}
		r, err := c.do(k.method, path, body, nil, k.o)
		if err != nil {
			return err
		}
		if r.Status != k.status || k.code != "" && r.Code != k.code {
			return fmt.Errorf("negative check %q: got %d %q, want %d %q", k.name, r.Status, r.Code, k.status, k.code)
		}
	}
	return nil
}
