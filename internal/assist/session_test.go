package assist

import (
	"fmt"
	"testing"
	"time"
)

func TestSessionScopesCapAndFreshness(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewSession(true)
	s.Now = func() time.Time { return now }
	if st := s.Settings(); st.Enabled || st.Handoff || st.Headless || !st.StrictLocal {
		t.Fatalf("%+v", st)
	}
	p := Package{Kind: KindPlan, Markdown: "x", SHA256: "h0"}
	s.Put("a", p, nil)
	if _, _, found, _ := s.Get("a", "h0"); found {
		t.Fatal("a package is readable while AI is off")
	}
	s.SetEnabled(true)
	// Strict local-only mode keeps handoff (OD-AI-4) and never headless.
	if st := s.Settings(); !st.Handoff || st.Headless {
		t.Fatalf("%+v", st)
	}
	s.Put("a", p, nil)
	if _, _, found, _ := s.Get("b", "h0"); found {
		t.Fatal("a package crossed scopes")
	}
	stale := false
	s.Put("a", Package{Kind: KindDrift, Markdown: "y", SHA256: "h1"}, func() bool { return !stale })
	stale = true
	if _, _, found, fresh := s.Get("a", "h1"); !found || fresh {
		t.Fatal("a stale package is fresh")
	}
	for i := 2; i < maxPackages+2; i++ {
		now = now.Add(time.Second)
		s.Put("a", Package{SHA256: fmt.Sprint("h", i)}, nil)
	}
	if len(s.packages) != maxPackages {
		t.Fatalf("%d packages held", len(s.packages))
	}
	if _, _, found, _ := s.Get("a", "h0"); found {
		t.Fatal("the oldest package was not evicted")
	}
	if _, md, found, fresh := s.Get("a", fmt.Sprint("h", maxPackages+1)); !found || !fresh || md != "" {
		t.Fatal("the newest package is missing")
	}
}
