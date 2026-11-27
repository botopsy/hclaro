package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serve(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "hclaro/") || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected request data: %v %q", r.Header, r.URL)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestStatus(t *testing.T) {
	good := releasePrefix + "tag/v1.1.0"
	cases := []struct {
		name, current, body string
		available           bool
		url                 string
	}{
		{"newer", "1.0.0", `{"tag_name":"v1.1.0","html_url":"` + good + `"}`, true, good},
		{"same", "1.0.0", `{"tag_name":"v1.0.0","html_url":"` + good + `"}`, false, ""},
		{"older", "1.0.0", `{"tag_name":"v0.9.0"}`, false, ""},
		{"foreign url dropped", "1.0.0", `{"tag_name":"v1.1.0","html_url":"https://evil.example/x"}`, true, ""},
		{"prerelease ignored", "1.0.0", `{"tag_name":"v2.0.0","prerelease":true}`, false, ""},
		{"draft ignored", "1.0.0", `{"tag_name":"v2.0.0","draft":true}`, false, ""},
		{"malformed", "1.0.0", `{nope`, false, ""},
		{"oversized", "1.0.0", `{"tag_name":"v2.0.0","x":"` + strings.Repeat("a", maxBody) + `"}`, false, ""},
		{"bad tag", "1.0.0", `{"tag_name":"latest"}`, false, ""},
		{"beta to release", "1.0.0-beta.2", `{"tag_name":"v1.0.0"}`, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			s := serve(t, c.body, &hits)
			k := &Checker{Current: c.current, URL: s.URL}
			got := k.Status(context.Background())
			if got.Available != c.available || got.URL != c.url || !got.Enabled {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestDevBuildNeverChecks(t *testing.T) {
	var hits atomic.Int32
	s := serve(t, `{"tag_name":"v9.9.9"}`, &hits)
	if got := (&Checker{Current: "dev", URL: s.URL}).Status(context.Background()); got.Available || hits.Load() != 0 {
		t.Fatalf("got %+v after %d requests", got, hits.Load())
	}
}

func TestNilCheckerIsDisabledAndCachingHolds(t *testing.T) {
	if (*Checker)(nil).Status(context.Background()).Enabled {
		t.Fatal("nil checker must be disabled")
	}
	var hits atomic.Int32
	s := serve(t, `{"tag_name":"v1.1.0"}`, &hits)
	k := &Checker{Current: "1.0.0", URL: s.URL}
	for range 3 {
		k.Status(context.Background())
	}
	if hits.Load() != 1 {
		t.Fatalf("%d requests, want 1 within the cache window", hits.Load())
	}
	k.Every = time.Nanosecond
	time.Sleep(time.Millisecond)
	k.Status(context.Background())
	if hits.Load() != 2 {
		t.Fatalf("%d requests, want a refresh after Every", hits.Load())
	}
}

func TestServerErrorAndTimeoutReadAsNoUpdate(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer s.Close()
	if (&Checker{Current: "1.0.0", URL: s.URL}).Status(context.Background()).Available {
		t.Fatal("500 must not report an update")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if (&Checker{Current: "1.0.0", URL: s.URL}).Status(ctx).Available {
		t.Fatal("cancelled lookup must not report an update")
	}
}

func TestLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.0.0", "1.0.1", true}, {"1.9.0", "1.10.0", true}, {"1.0.0-beta.1", "1.0.0", true}, {"1.0.0", "1.0.0-beta.1", false}, {"1.0.0", "1.0.0", false}, {"2.0.0", "1.9.9", false}} {
		a, _ := parse(c.a)
		b, _ := parse(c.b)
		if less(a, b) != c.want {
			t.Errorf("less(%s,%s) != %v", c.a, c.b, c.want)
		}
	}
	for _, bad := range []string{"", "dev", "1.0", "1.0.x", "01.0.0"} {
		if _, ok := parse(bad); ok {
			t.Errorf("parse(%q) should fail", bad)
		}
	}
}
