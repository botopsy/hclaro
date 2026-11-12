package api

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"hclaro/internal/operations"
	"hclaro/internal/profiles"
)

// security.Guard treats every POST, PATCH and DELETE under /api/ as mutating and leaves the
// choice of routes to the router. These tests drive the real handler with full credentials
// to prove the router is the second allowlist: every route names its methods, intents that
// were never routes stay unrouted, and a malformed ID never reaches an action.

// dynamicRoutes lists the registrations whose pattern is built at run time, keyed by the
// pattern expression as written in the source, with the mutating patterns each one expands
// to. A new built pattern fails TestEveryRouteNamesItsMethod until it is added here, and
// TestDynamicRoutesAreRouted fails when an expansion is no longer served.
// shortcut: a new prefix in gitRoutes' `both` map or a new tool in the projectRoutes loop keeps
// the expression text, so it is not detected and its routes miss the malformed-ID sweeps;
// the method guard still protects them. Upgrade to a recorded registration if this grows.
var dynamicRoutes = map[string][]string{
	`"GET " + prefix + "/git/head"`:     nil,
	`"POST " + prefix + "/git/status"`:  {"POST /api/v1/git/status", "POST /api/v1/projects/{id}/git/status"},
	`"POST " + prefix + "/git/diff"`:    {"POST /api/v1/git/diff", "POST /api/v1/projects/{id}/git/diff"},
	`"POST " + prefix + "/editor/open"`: {"POST /api/v1/editor/open", "POST /api/v1/projects/{id}/editor/open"},
	`"POST /api/v1/projects/{id}/operations/" + string(tool)`: {
		"POST /api/v1/projects/{id}/operations/validate", "POST /api/v1/projects/{id}/operations/fmt-check",
		"POST /api/v1/projects/{id}/operations/providers", "POST /api/v1/projects/{id}/operations/outputs-metadata",
	},
}

// registrations parses this package's sources and returns every literal route pattern and
// the source text of every pattern built at run time.
func registrations(t *testing.T) (literal []string, dynamic map[string]bool) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	dynamic = map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle" {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				pattern, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				literal = append(literal, pattern)
			} else {
				dynamic[types.ExprString(call.Args[0])] = true
			}
			return true
		})
	}
	if len(literal) < 100 {
		t.Fatalf("only %d literal routes found; the source scan is broken", len(literal))
	}
	return literal, dynamic
}

// mutatingPatterns returns every registered POST, PATCH and DELETE pattern.
func mutatingPatterns(t *testing.T) []string {
	t.Helper()
	literal, _ := registrations(t)
	var out []string
	for _, p := range literal {
		if strings.HasPrefix(p, "POST ") || strings.HasPrefix(p, "PATCH ") || strings.HasPrefix(p, "DELETE ") {
			out = append(out, p)
		}
	}
	for _, expansions := range dynamicRoutes {
		out = append(out, expansions...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

var methodPrefix = regexp.MustCompile(`^(GET|POST|PATCH|DELETE) /api/`)

// Every route but the two catch-alls names its method, so the router refuses other methods
// and a read route can never receive a mutation that passed the guard.
func TestEveryRouteNamesItsMethod(t *testing.T) {
	literal, dynamic := registrations(t)
	for _, p := range literal {
		if p != "/" && p != "/api/" && !methodPrefix.MatchString(p) {
			t.Errorf("route %q does not name its method", p)
		}
	}
	for expr := range dynamic {
		if _, ok := dynamicRoutes[expr]; !ok {
			t.Errorf("route pattern %s is built at run time; add it and its expansions to dynamicRoutes", expr)
		}
	}
	for expr := range dynamicRoutes {
		if !dynamic[expr] {
			t.Errorf("dynamicRoutes lists %s, which is no longer registered", expr)
		}
	}
}

// Each hand-listed expansion is served: a request reaches a handler, not the catch-all.
func TestDynamicRoutesAreRouted(t *testing.T) {
	s := newHubServer(t)
	for _, expansions := range dynamicRoutes {
		for _, pattern := range expansions {
			method, path, _ := strings.Cut(pattern, " ")
			path = strings.ReplaceAll(path, "{id}", strings.Repeat("0", 32))
			if w := s.do(method, path, "{}"); refusal(w.Body.Bytes()) == "route_not_found" || w.Code == 405 {
				t.Errorf("%s is not routed: %d %s", pattern, w.Code, w.Body)
			}
		}
	}
}

func TestReadOnlyRoutesAreNotMutable(t *testing.T) {
	s := newHubServer(t)
	for _, path := range []string{"/api/v1/health", "/api/v1/workspace", "/api/v1/resources", "/api/v1/resources/x"} {
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			if w := s.do(method, path, "{}"); w.Code != 404 || refusal(w.Body.Bytes()) != "route_not_found" {
				t.Errorf("%s %s: %d %s, want 404 route_not_found", method, path, w.Code, w.Body)
			}
		}
	}
}

// Intents that were never routes. The router answers each one; no handler runs.
func TestUnroutedMutationsAreRefused(t *testing.T) {
	s := newHubServer(t)
	id := strings.Repeat("0", 32)
	for _, path := range []string{
		"/api/v1/plans/" + id + "/destroy",
		"/api/v1/git/checkout",
		"/api/v1/editor/run",
		"/api/v1/imports/write",
		"/api/v1/projects/" + id + "/git/commit",
		"/api/v1/contexts/" + id + "/plans/" + id + "/destroy",
		"/api/v1/contexts/" + id + "/discovery/run",
		"/api/v1/contexts/" + id + "/imports/run",
	} {
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			if w := s.do(method, path, "{}"); w.Code != 404 || refusal(w.Body.Bytes()) != "route_not_found" {
				t.Errorf("%s %s: %d %s, want 404 route_not_found", method, path, w.Code, w.Body)
			}
		}
	}
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("an unrouted request ran %d engine commands", n)
	}
}

// Go unescapes path wildcards, so an encoded slash or dot segment arrives in PathValue. No
// mutating route may succeed or fail internally with one. This sweep sends an empty body, so
// it only proves the request is refused; TestMalformedIDsCannotMutateProjects and
// TestScopedNearMissIDsReachNoPlan prove which layer refuses it when the body is valid.
// History routes are not registered on this server; TestHistoryDeleteClearAndRetention sends
// near-miss history IDs to DELETE with a valid body.
func TestMalformedIDsNeverSucceed(t *testing.T) {
	s := newHubServer(t)
	s.hub.ScopedExecution = true // otherwise every scoped route stops at the gate
	s.add("network")
	bad := []string{"%2e%2e", "%2F", "..%2Fx", strings.Repeat("A", 32), strings.Repeat("0", 31), strings.Repeat("0", 64), "%00", "x%20y", "%E2%82%AC"}
	wildcard := regexp.MustCompile(`\{[a-z]+\}`)
	for _, pattern := range mutatingPatterns(t) {
		method, path, _ := strings.Cut(pattern, " ")
		if !strings.Contains(path, "{") {
			continue // no ID to malform, and browse would list a real directory
		}
		for _, b := range bad {
			p := wildcard.ReplaceAllString(path, b)
			if u, err := url.ParseRequestURI(p); err != nil || u.Path == "" {
				continue
			}
			w := s.do(method, p, "{}")
			if w.Code < 300 && w.Code >= 200 || w.Code >= 500 || refusal(w.Body.Bytes()) == "scoped_execution_disabled" {
				t.Errorf("%s %s: %d %s", method, p, w.Code, w.Body)
			}
		}
	}
}

func refusal(body []byte) string {
	var b struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Error
}

// layer names what refused a request: the router (path cleaning or the catch-all), the body
// decoder, or the ID lookup.
func layer(code int, body []byte) string {
	if code >= 300 && code < 400 {
		return "router"
	}
	switch e := refusal(body); e {
	case "route_not_found":
		return "router"
	case "invalid_request":
		return "decoder"
	case "project_not_found", "profile_not_found", "plan_not_found", "context_closed", "operation_not_found", "history_not_found":
		return "id"
	default:
		return fmt.Sprintf("%d %s", code, e)
	}
}

// nearMisses are look-alikes of a real ID: other case, one character more, less or
// different, encoded separators and dot segments, and an empty segment.
func nearMisses(real string) []string {
	// sibling has a valid format, so it passes any format check and tests the lookup itself.
	last := "0"
	if strings.HasSuffix(real, "0") {
		last = "1"
	}
	sibling := real[:len(real)-1] + last
	return []string{sibling, strings.ToUpper(real), real + "0", real[:len(real)-1], "x" + real[1:], real + "%2F", "%2F" + real,
		real + "%00", real + "%20", "..%2F" + real, "%2e%2e", "", real + "/"}
}

// snapshot is everything a project or profile mutation can change: the registry and
// profile files (names and contents) and the number of engine commands run.
func snapshot(t *testing.T, s *hubServer, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{"engine calls": strconv.FormatInt(s.calls.Load(), 10)}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[filepath.Join(dir, e.Name())] = string(b)
		}
	}
	return out
}

// Near misses of a real ID, encoded separators and dot segments, sent with a body the route
// accepts, never mutate a project or profile or run the engine. Each is refused by the router
// or the ID lookup, never by the decoder, and the same request with the real ID succeeds and
// changes the snapshot, so the comparison can fail.
func TestMalformedIDsCannotMutateProjects(t *testing.T) {
	s := newHubServer(t)
	profileDir := t.TempDir()
	s.hub.Profiles = profiles.Open(profileDir, time.Now())
	_, _, registryPath := s.hub.Registry.Status()
	dirs := []string{filepath.Dir(registryPath), profileDir}
	project := s.add("network").ID
	w := s.do("POST", "/api/v1/projects/"+project+"/execution-profiles", `{"name":"Production","workspace":"prod"}`)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	profile := decode[profileView](t, w).ID
	for _, step := range []struct {
		method, path, body string
		want               int
	}{
		{"PATCH", "/api/v1/projects/{id}", `{"alias":"Renamed"}`, 200},
		{"POST", "/api/v1/projects/{id}/inspect", `{}`, 200},
		{"POST", "/api/v1/projects/{id}/execution-profiles", `{"name":"Staging","workspace":"dev"}`, 201},
		{"PATCH", "/api/v1/projects/{id}/execution-profiles/{pid}", `{"name":"Production","workspace":"dev","expectedRevision":1}`, 200},
		{"POST", "/api/v1/projects/{id}/execution-profiles/{pid}/clone", `{"name":"Copy"}`, 201},
		{"POST", "/api/v1/projects/{id}/execution-profiles/default", `{"profileId":"` + profile + `"}`, 200},
		{"DELETE", "/api/v1/projects/{id}/execution-profiles/{pid}", `{"expectedRevision":2}`, 200},
		{"DELETE", "/api/v1/projects/{id}", `{}`, 200},
	} {
		real := map[string]string{"{id}": project, "{pid}": profile}
		before := snapshot(t, s, dirs...)
		for slot, value := range real {
			if !strings.Contains(step.path, slot) {
				continue
			}
			for _, variant := range nearMisses(value) {
				path := strings.ReplaceAll(step.path, slot, variant)
				for other, v := range real {
					path = strings.ReplaceAll(path, other, v)
				}
				w := s.do(step.method, path, step.body)
				if l := layer(w.Code, w.Body.Bytes()); l != "router" && l != "id" {
					t.Errorf("%s %s: refused by %s, want the router or the ID lookup: %s", step.method, path, l, w.Body)
				}
			}
		}
		if after := snapshot(t, s, dirs...); !maps.Equal(before, after) {
			t.Fatalf("%s %s: a malformed ID changed the project, its profiles or ran the engine", step.method, step.path)
		}
		path := strings.NewReplacer("{id}", project, "{pid}", profile).Replace(step.path)
		if w := s.do(step.method, path, step.body); w.Code != step.want {
			t.Fatalf("control %s %s: %d %s, want %d", step.method, path, w.Code, w.Body, step.want)
		}
		if after := snapshot(t, s, dirs...); maps.Equal(before, after) {
			t.Fatalf("control %s %s changed nothing, so the snapshot cannot detect a mutation", step.method, path)
		}
	}
}

// The execution routes of a real scoped plan: preflight, approval, apply and cancel, each sent
// with near misses of the plan, context and operation IDs and a body the route accepts. Every
// one stops at the router or the ID lookup and nothing is approved or applied; the same step
// with the real IDs then succeeds, so the body was valid.
func TestScopedNearMissIDsReachNoPlan(t *testing.T) {
	s := newScopedServer(t)
	cid := s.openLoaded("network", "dev", "")
	plan := s.plan(cid)
	done := s.waitScoped(cid, plan.ID)
	if done.Status != operations.Succeeded || done.PlanID == "" {
		t.Fatalf("plan: %+v", done)
	}
	pid := done.PlanID
	refused := func(method, template, body string, ids map[string]string) {
		t.Helper()
		for slot, value := range ids {
			for _, variant := range nearMisses(value) {
				path := strings.ReplaceAll(template, slot, variant)
				for other, v := range ids {
					path = strings.ReplaceAll(path, other, v)
				}
				w := s.do(method, path, body)
				if l := layer(w.Code, w.Body.Bytes()); l != "router" && l != "id" {
					t.Errorf("%s %s: refused by %s, want the router or the ID lookup: %s", method, path, l, w.Body)
				}
			}
		}
		if n := len(s.calls("apply")); n != 0 {
			t.Fatalf("%s %s: a near-miss ID ran %d applies", method, template, n)
		}
	}
	ids := map[string]string{"{cid}": cid, "{pid}": pid}
	step := "/api/v1/contexts/{cid}/plans/{pid}/"
	real := "/api/v1/contexts/" + cid + "/plans/" + pid + "/"

	refused("POST", step+"preflight", `{}`, ids)
	w := s.do("POST", real+"preflight", `{}`)
	if w.Code != 200 {
		t.Fatalf("preflight: %d %s", w.Code, w.Body)
	}
	report := decode[scopedPreflight](t, w)
	ack, _ := jsonString(report.Acknowledge)
	approval := fmt.Sprintf(`{"preflightBinding":%q,"acknowledged":%s}`, report.Binding, ack)

	refused("POST", step+"approval", approval, ids)
	w = s.do("POST", real+"approval", approval)
	if w.Code != 200 {
		t.Fatalf("approval: %d %s", w.Code, w.Body)
	}
	apply := applyBody(decode[scopedApproval](t, w))

	refused("POST", step+"apply", apply, ids)
	refused("POST", "/api/v1/contexts/{cid}/operations/{oid}/cancel", `{}`, map[string]string{"{cid}": cid, "{oid}": plan.ID})
	if w := s.do("POST", "/api/v1/contexts/"+cid+"/operations/"+plan.ID+"/cancel", `{}`); w.Code != 409 || refusal(w.Body.Bytes()) != "operation_not_running" {
		t.Fatalf("cancel of the finished plan: %d %s", w.Code, w.Body)
	}
	w = s.do("POST", real+"apply", apply)
	if w.Code != 202 {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	if done := s.waitScoped(cid, decode[scopedOp](t, w).ID); done.Status != operations.Succeeded || len(s.calls("apply")) != 1 {
		t.Fatalf("control apply: %+v, %d apply calls", done, len(s.calls("apply")))
	}
}
