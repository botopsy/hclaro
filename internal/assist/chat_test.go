package assist

import (
	"strings"
	"testing"
)

// AI-01.09: a conversation without a package sends no project data; attaching a project package
// is an explicit turn whose bytes are exactly the previewed package.
func TestChatWithoutProjectDataThenAttach(t *testing.T) {
	good := &stub{id: "good", mode: ModeHeadless}
	s := ready(t, good)
	scope := RunScope{Project: "net", Workspace: "default", Root: "/synthetic/net"}
	var records []RunRecord
	s.Record = func(r RunRecord) { records = append(records, r) }

	_, err := s.StartChat("a", DisclosureVersion, scope, "", nil)
	refused(t, err, RefuseMessageEmpty)
	_, err = s.StartChat("a", DisclosureVersion, scope, "my key is AKIAIOSFODNN7EXAMPLE", nil)
	refused(t, err, RefuseMessageUnsafe)
	_, err = s.StartChat("a", "3", scope, "hi", nil)
	refused(t, err, RefuseDisclosure)

	c, err := s.StartChat("a", DisclosureVersion, scope, "What is a moved block?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindChat || c.SHA256 != "" {
		t.Fatalf("kind %q sha %q", c.Kind, c.SHA256)
	}
	settle(t, s, "a", c.ID)
	first := good.calls()[0]
	if !strings.Contains(first, "No project data is attached") || strings.Contains(first, "## Context package") || strings.Contains(first, pkgText) {
		t.Fatalf("conversation-only stdin carries project data:\n%s", first)
	}

	// Attach: only a stored project package of this scope, and nothing runs.
	project := "# Hclaro context: project\n\n```hcl\nresource \"null_resource\" \"a\" {}\n```\n"
	s.Put("a", Package{Kind: KindProject, Markdown: project, SHA256: hashOf(project)}, nil)
	_, err = s.Attach("a", c.ID, "h1") // the plan package
	refused(t, err, RefuseInvalidArgs)
	_, err = s.Attach("b", c.ID, hashOf(project))
	refused(t, err, RefuseNoChat)
	_, err = s.Attach("a", c.ID, hashOf("nope"))
	refused(t, err, RefuseNotFound)
	c, err = s.Attach("a", c.ID, hashOf(project))
	if err != nil {
		t.Fatal(err)
	}
	last := c.Turns[len(c.Turns)-1]
	if last.Role != "context" || last.SHA256 != hashOf(project) || last.Text != "" || c.Kind != KindProject || len(good.calls()) != 1 {
		t.Fatalf("attach: %+v kind %s calls %d", last, c.Kind, len(good.calls()))
	}
	if _, err := s.Send("a", c.ID, "Which resources does it define?"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, "a", c.ID)
	second := good.calls()[1]
	if !strings.HasPrefix(second, Transcript("", c.Turns[:2])) {
		t.Fatal("the second turn does not replay the first")
	}
	if !strings.Contains(second, "## Turn 3: Hclaro attached project context (sha256 "+hashOf(project)+")\n\n````markdown\n"+project+"\n````\n") {
		t.Fatalf("attached package is not in the transcript verbatim:\n%s", second)
	}
	if len(records) != 2 || records[0].Kind != KindChat || records[1].Kind != KindProject {
		t.Fatalf("records %+v", records)
	}

	// A stale project package refuses the attach and later turns.
	s.Put("a", Package{Kind: KindProject, Markdown: "old", SHA256: hashOf("old")}, func() bool { return false })
	_, err = s.Attach("a", c.ID, hashOf("old"))
	refused(t, err, RefuseStale)
}

// The list shows every conversation of the session; only those of the current scope and
// workspace are "here". A launch conversation-only chat goes stale when the workspace changes.
func TestChatListAndWorkspaceSwitch(t *testing.T) {
	good := &stub{id: "good", mode: ModeHeadless}
	s := ready(t, good)
	ws := "default"
	launch, err := s.StartChat("launch", DisclosureVersion, RunScope{Project: "net", Workspace: "default"}, "hello", func() bool { return ws == "default" })
	if err != nil {
		t.Fatal(err)
	}
	hub, err := s.StartChat("cid1", DisclosureVersion, RunScope{Project: "app", Workspace: "prod"}, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	settle(t, s, "launch", launch.ID)
	settle(t, s, "cid1", hub.ID)
	list := s.List("launch", "default")
	if len(list) != 2 {
		t.Fatalf("list %+v", list)
	}
	for _, l := range list {
		if l.Here != (l.ID == launch.ID) {
			t.Fatalf("here flag wrong: %+v", l)
		}
		if l.ID == hub.ID && (l.Project != "app" || l.Workspace != "prod" || l.Title != "hi") {
			t.Fatalf("hub entry %+v", l)
		}
	}
	// Another project's conversation cannot be continued from here.
	_, err = s.Send("launch", hub.ID, "more")
	refused(t, err, RefuseNoChat)

	ws = "staging"
	if l := s.List("launch", "staging"); l[0].Here || l[1].Here {
		t.Fatal("a conversation of another workspace is listed as current")
	}
	_, err = s.Send("launch", launch.ID, "still there?")
	refused(t, err, RefuseStale)
	// Nothing was lost: switching back makes it current again.
	ws = "default"
	if _, err := s.Send("launch", launch.ID, "back again"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, "launch", launch.ID)
	if _, _ = s.SetEnabled(false); len(s.List("launch", "default")) != 0 {
		t.Fatal("turning AI off kept conversations")
	}
}

// A conversation whose project package went stale continues only after current project context
// is attached; one whose starting context changed never does.
func TestReattachProjectContext(t *testing.T) {
	good := &stub{id: "good", mode: ModeHeadless}
	s := ready(t, good)
	files := "v1"
	pkg := func(text string) string {
		md := "# Hclaro context: project\n\n" + text + "\n"
		built := files
		s.Put("a", Package{Kind: KindProject, Markdown: md, SHA256: hashOf(md)}, func() bool { return files == built })
		return hashOf(md)
	}
	c, err := s.StartConversation("a", pkg("v1"), DisclosureVersion, RunScope{Project: "net"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	settle(t, s, "a", c.ID)
	files = "v2" // a .tf file changed on disk
	if c, _ := s.Conversation("a", c.ID); !c.Stale {
		t.Fatal("changed files did not make the conversation stale")
	}
	_, err = s.Send("a", c.ID, "still?")
	refused(t, err, RefuseStale)
	if _, err := s.Attach("a", c.ID, pkg("v2")); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Conversation("a", c.ID); c.Stale {
		t.Fatal("current project context did not make the conversation current")
	}
	if _, err := s.Send("a", c.ID, "and now?"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, "a", c.ID)

	// A plan review whose plan changed is not revived by project context.
	planCurrent := true
	s.Put("a", Package{Kind: KindPlan, Markdown: "plan", SHA256: hashOf("plan")}, func() bool { return planCurrent })
	r, err := s.StartConversation("a", hashOf("plan"), DisclosureVersion, RunScope{}, "")
	if err != nil {
		t.Fatal(err)
	}
	settle(t, s, "a", r.ID)
	planCurrent = false
	_, err = s.Attach("a", r.ID, pkg("v3"))
	refused(t, err, RefuseStale)
}
