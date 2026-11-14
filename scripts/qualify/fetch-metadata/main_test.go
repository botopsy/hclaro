package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Offline self-test against a local TLS server: allowed text is fetched; archive URLs, binary
// content types, oversize bodies and off-allowlist redirects are refused, and all are logged.
func TestSeparationRule(t *testing.T) {
	hits := map[string]int{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/ok/SHA256SUMS":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("abc  x.zip\n"))
		case "/ok/v2.json":
			w.Header().Set("Content-Type", "application/vnd.api+json")
			w.Write([]byte(`{}`))
		case "/ok/binary":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte{0, 1, 2})
		case "/ok/big":
			w.Header().Set("Content-Type", "text/plain")
			w.Write(bytes.Repeat([]byte("a"), maxBody+1))
		case "/ok/away":
			http.Redirect(w, r, "https://elsewhere.invalid/file.txt", http.StatusFound)
		case "/ok/here":
			http.Redirect(w, r, "/ok/SHA256SUMS", http.StatusFound)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	out := t.TempDir()
	os.MkdirAll(filepath.Join(out, "bodies"), 0o700)
	var log bytes.Buffer
	f := newFetcher([]rule{{"test", host, "/ok/"}}, srv.Client(), out, &log)

	cases := map[string]string{
		srv.URL + "/ok/SHA256SUMS":            "fetched",
		srv.URL + "/ok/v2.json":               "fetched",
		srv.URL + "/ok/here":                  "fetched",
		srv.URL + "/ok/pkg_1.0_linux.zip":     "refused:archive",
		srv.URL + "/ok/pkg.ZIP":               "refused:archive",
		srv.URL + "/ok/binary":                "refused:content-type",
		srv.URL + "/ok/big":                   "refused:size",
		srv.URL + "/ok/away":                  "refused:redirect host elsewhere.invalid",
		srv.URL + "/other/SHA256SUMS":         "refused:host",
		"http://" + host + "/ok/SHA256SUMS":   "refused:scheme",
		"https://u:p@" + host + "/ok/SHA256S": "refused:scheme",
	}
	for u, want := range cases {
		if got := f.fetch(u); got.Result != want {
			t.Errorf("%s: %s, want %s", u, got.Result, want)
		}
	}
	for _, p := range []string{"/ok/pkg_1.0_linux.zip", "/ok/pkg.ZIP", "/other/SHA256SUMS"} {
		if hits[p] != 0 {
			t.Errorf("%s was requested; must be refused before any request", p)
		}
	}

	var n int
	sc := bufio.NewScanner(&log)
	for sc.Scan() {
		var e entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		n++
		if e.Result != "fetched" && e.Body != "" {
			t.Errorf("refused %s kept a body", e.URL)
		}
		if e.Result == "fetched" {
			if _, err := os.Stat(filepath.Join(out, e.Body)); err != nil {
				t.Errorf("fetched body missing: %v", err)
			}
		}
	}
	if n != len(cases) {
		t.Fatalf("logged %d attempts, want %d", n, len(cases))
	}
	entries, _ := os.ReadDir(filepath.Join(out, "bodies"))
	if len(entries) != 2 { // SHA256SUMS (twice, same hash) and v2.json
		t.Fatalf("bodies = %d, want 2", len(entries))
	}
}

func TestAllowlistIsPathScoped(t *testing.T) {
	f := newFetcher(od1a, http.DefaultClient, "", &bytes.Buffer{})
	for raw, ok := range map[string]bool{
		"https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS":                                        true,
		"https://github.com/someone/else/releases":                                                                                     false,
		"https://registry.terraform.io/v1/providers/hashicorp/random/versions":                                                         true,
		"https://registry.terraform.io/v1/providers/hashicorp/aws/versions":                                                            false,
		"https://releases.hashicorp.com/terraform/1.5.5/terraform_1.5.5_SHA256SUMS":                                                    true,
		"https://releases.hashicorp.com/terraform/1.5.5/terraform_1.5.5_darwin_amd64.zip":                                              false,
		"https://releases.hashicorp.com/vault/":                                                                                        false,
		"https://objects.githubusercontent.com/github-production-release-asset/tofu_1.13.9_SHA256SUMS":                                 false,
		"https://github.com/opentofu/terraform-provider-random/releases/download/v3.9.1/terraform-provider-random_3.9.1_SHA256SUMS":    true,
		"https://github.com/opentofu/terraform-provider-local/releases/download/v2.9.1/terraform-provider-local_2.9.1_SHA256SUMS.sig":  true,
		"https://github.com/opentofu/terraform-provider-random/releases/download/v3.9.1/terraform-provider-random_3.9.1_manifest.json": false,
		"https://github.com/opentofu/terraform-provider-random/releases":                                                               false,
		"https://github.com/opentofu/terraform-provider-aws/releases/download/v1/x_SHA256SUMS":                                         false,
	} {
		u, _ := url.Parse(raw)
		if _, reason := f.check(u); (reason == "") != ok {
			t.Errorf("%s: reason %q, want allowed=%v", raw, reason, ok)
		}
	}
}

func TestExceptionsAreNarrow(t *testing.T) {
	mustURL := func(s string) *url.URL {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	asset := mustURL("https://release-assets.githubusercontent.com/github-production-release-asset/123?sig=x")
	for raw, ok := range map[string]bool{ // E2
		"https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS":                                                   true,
		"https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS.gpgsig":                                            true,
		"https://github.com/hashicorp/terraform-provider-random/releases/download/v3.6.0/terraform-provider-random_3.6.0_SHA256SUMS.72D7468F.sig": true,
		"https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_darwin_amd64.tar.gz":                                          false,
		"https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS.pem":                                               false,
		"https://github.com/someone/else/releases/download/v1/x_SHA256SUMS":                                                                       false,
	} {
		if got := releaseAssetRedirect(mustURL(raw), asset); got != ok {
			t.Errorf("E2 %s: %v, want %v", raw, got, ok)
		}
	}
	tofuSums := mustURL("https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS")
	if releaseAssetRedirect(tofuSums, mustURL("https://objects.githubusercontent.com/x")) {
		t.Error("E2 must allow only the release-asset host")
	}
	if releaseAssetRedirect(tofuSums, mustURL("http://release-assets.githubusercontent.com/x")) {
		t.Error("E2 must require https")
	}

	sig := mustURL("https://releases.hashicorp.com/terraform/1.5.5/terraform_1.5.5_SHA256SUMS.72D7468F.sig")
	if !octetAllowed(sig, make([]byte, 566)) { // E1
		t.Error("E1: signature refused")
	}
	if octetAllowed(sig, make([]byte, maxSig+1)) {
		t.Error("E1: oversize signature accepted")
	}
	sums := mustURL("https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS")
	line := strings.Repeat("ab", 32) + "  tofu_1.13.9_darwin_amd64.zip\n"
	if !octetAllowed(sums, []byte(line+line)) {
		t.Error("E1 extension: checksum lines refused")
	}
	for _, bad := range []string{"x", line + "junk\n", strings.Repeat("ab", 32) + "  ../x\n", strings.Repeat(line, maxSig/len(line)+1), "\x50\x4b\x03\x04"} {
		if octetAllowed(sums, []byte(bad)) {
			t.Errorf("E1 extension accepted %.40q", bad)
		}
	}
	reg := mustURL("https://registry.opentofu.org/v1/providers/hashicorp/random/versions")
	if !octetAllowed(reg, []byte(`{"versions":[]}`)) { // E3
		t.Error("E3: JSON refused")
	}
	if octetAllowed(reg, []byte{0x50, 0x4b, 3, 4}) {
		t.Error("E3: non-JSON accepted")
	}
	if octetAllowed(mustURL("https://registry.terraform.io/v1/providers/hashicorp/random/versions"), []byte(`{}`)) {
		t.Error("E3 covers registry.opentofu.org only")
	}
}

// An octet-stream signature through the full fetch path is kept; any other octet body is not.
func TestOctetSignatureFetch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("sig"))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	out := t.TempDir()
	os.MkdirAll(filepath.Join(out, "bodies"), 0o700)
	f := newFetcher([]rule{{"test", host, "/"}}, srv.Client(), out, &bytes.Buffer{})
	if got := f.fetch(srv.URL + "/x_1.0_SHA256SUMS.sig").Result; got != "fetched" {
		t.Errorf("signature: %s", got)
	}
	if got := f.fetch(srv.URL + "/x_1.0_SHA256SUMS").Result; got != "refused:content-type" {
		t.Errorf("octet SHA256SUMS that is not checksum lines: %s", got)
	}
}
