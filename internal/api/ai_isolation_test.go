package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"hclaro/internal/assist"
)

// AI-01.13 isolation suite: the launch project and a Hub with two projects, one of them in two
// workspaces, each hold a saved conversation with a pending proposed change. Every route that
// names a conversation, asked from any other scope, is refused and changes nothing.

// aiConversationRoutes are the routes taking a conversation ID, relative to "/api/v1" or
// "/api/v1/contexts/{cid}". TestAIEveryConversationRouteRefusesAnotherScope fails when assist.go
// registers one that is not listed here.
var aiConversationRoutes = []struct{ method, path, body string }{
	{"GET", "/ai/conversations/{id}", ""},
	{"POST", "/ai/conversations/{id}/messages", `{"text":"hello from elsewhere"}`},
	{"POST", "/ai/conversations/{id}/context", `{"packageSha256":"{pkg}"}`},
	{"POST", "/ai/conversations/{id}/cancel", `{}`},
	{"POST", "/ai/conversations/{id}/close", `{}`},
	{"POST", "/ai/conversations/{id}/save", `{"saved":false}`},
	{"GET", "/ai/conversations/{id}/changes", ""},
	{"POST", "/ai/conversations/{id}/changes/apply", "{change}"},
	{"POST", "/ai/conversations/{id}/changes/reject", "{change}"},
	{"POST", "/ai/conversations/{id}/changes/undo", "{change}"},
	{"POST", "/ai/saved/{id}/open", `{}`},
	{"POST", "/ai/saved/{id}/delete", `{}`},
}

func TestAIEveryConversationRouteRefusesAnotherScope(t *testing.T) {
	src, err := os.ReadFile("assist.go")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, r := range aiConversationRoutes {
		listed[r.method+" "+r.path] = true
	}
	for _, m := range regexp.MustCompile(`HandleFunc\("(\w+) /api/v1(?:/contexts/\{cid\})?(/ai/[^"]*\{id\}[^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		if !listed[m[1]+" "+m[2]] {
			t.Errorf("route %s %s has no isolation case", m[1], m[2])
		}
	}

	s := newScopedServer(t)
	const proposed = "resource \"aws_s3_bucket\" \"isolated\" {}\n"
	stub := &stubAssistant{mode: assist.ModeHeadless, reply: proposalReply("isolated.tf", "new", proposed)}
	s.launch.Assist = assist.NewSession(false, stub)
	config := t.TempDir()
	s.launch.Assist.Store = assist.OpenStore(config, time.Now())
	turnOnAI(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) })
	s.do("POST", "/api/v1/ai/settings", `{"acceptDisclosure":"`+assist.DisclosureVersion+`"}`)

	type scope struct{ name, base, project, conversation, pkg string }
	scopes := []*scope{
		{name: "launch", base: "/api/v1", project: "launch"},
		{name: "network/dev", base: "/api/v1/contexts/" + s.openLoaded("network", "dev", ""), project: "network"},
		{name: "network/prod", base: "/api/v1/contexts/" + s.openLoaded("network", "prod", ""), project: "network"},
		{name: "platform/staging", base: "/api/v1/contexts/" + s.openLoaded("platform", "staging", ""), project: "platform"},
	}
	snapshot := map[string]string{}
	for _, sc := range scopes {
		w := s.do("POST", sc.base+"/ai/conversations", chatBody("Question in "+sc.name))
		if w.Code != 202 {
			t.Fatalf("%s: %d %s", sc.name, w.Code, w.Body)
		}
		sc.conversation = decode[assist.Conversation](t, w).ID
		waitChat(t, func(m, p, b string) *httptest.ResponseRecorder { return s.do(m, p, b) }, sc.base, sc.conversation)
		if w := s.do("POST", sc.base+"/ai/conversations/"+sc.conversation+"/save", `{"saved":true}`); w.Code != 200 {
			t.Fatalf("save %s: %d %s", sc.name, w.Code, w.Body)
		}
		sc.pkg = build(t, s.do("POST", sc.base+"/ai/packages", `{"kind":"project"}`)).SHA256
		snapshot[sc.name] = s.do("GET", sc.base+"/ai/conversations/"+sc.conversation, "").Body.String()
	}
	change := `{"turn":1,"index":0,"baseSha256":"new","newSha256":"` + sha(proposed) + `"}`
	if v := decode[struct{ Changes []assist.ChangeView }](t, s.do("GET", scopes[1].base+"/ai/conversations/"+scopes[1].conversation+"/changes", "")); len(v.Changes) != 1 || v.Changes[0].Status != assist.ChangeReady {
		t.Fatalf("no pending proposal to attack: %+v", v)
	}
	ran, files := stub.ran(), len(savedFiles(t, config))

	for _, owner := range scopes {
		for _, from := range scopes {
			if from == owner {
				continue
			}
			for _, r := range aiConversationRoutes {
				path := from.base + strings.Replace(r.path, "{id}", owner.conversation, 1)
				body := strings.NewReplacer("{pkg}", from.pkg, "{change}", change).Replace(r.body)
				// A saved file is per project: another workspace of the same project may list
				// and reopen it read-only (TestAIHubSavedConversationsArePerProject); it is not
				// open there, and the open copy stays in its own scope.
				if strings.HasPrefix(r.path, "/ai/saved/") && from.project == owner.project {
					continue
				}
				w := s.do(r.method, path, body)
				// Delete is idempotent and only ever touches the asking project's own folder:
				// another project's ID is a no-op, checked by the snapshot below.
				if r.path == "/ai/saved/{id}/delete" && w.Code == 200 {
					continue
				}
				if w.Code != 404 || !strings.Contains(w.Body.String(), `"conversation_not_found"`) {
					t.Errorf("%s %s from %s (owner %s): %d %s", r.method, r.path, from.name, owner.name, w.Code, w.Body)
				}
			}
		}
	}

	// Nothing changed: no turn ran, no file was written or deleted, every conversation is as it was.
	if stub.ran() != ran || len(savedFiles(t, config)) != files {
		t.Fatalf("runs %d→%d, saved files %d→%d", ran, stub.ran(), files, len(savedFiles(t, config)))
	}
	for _, sc := range scopes {
		if got := s.do("GET", sc.base+"/ai/conversations/"+sc.conversation, "").Body.String(); got != snapshot[sc.name] {
			t.Fatalf("%s changed:\nbefore %s\nafter  %s", sc.name, snapshot[sc.name], got)
		}
	}
	for _, root := range []string{s.launch.Root(), s.roots["network"], s.roots["platform"]} {
		if _, err := os.Stat(filepath.Join(root, "isolated.tf")); err == nil {
			t.Fatalf("a proposal was written in %s", root)
		}
	}
}
