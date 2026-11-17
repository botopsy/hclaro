package assist

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// stub is an in-process Assistant for session tests.
type stub struct {
	id, mode string
	mu       sync.Mutex
	got      []string
	block    bool
}

func (s *stub) ID() string { return s.id }
func (s *stub) Detect(context.Context) ProviderStatus {
	return ProviderStatus{ID: s.id, Name: s.id, Mode: s.mode}
}
func (s *stub) Run(ctx context.Context, payload []byte) (string, error) {
	s.mu.Lock()
	s.got = append(s.got, string(payload))
	s.mu.Unlock()
	if s.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "reply to " + string(payload[:4]), nil
}

func wait(t *testing.T, s *Session) Run {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if r, ok := s.Current(); ok && r.Status != RunRunning {
			return r
		}
	}
	t.Fatal("run did not finish")
	return Run{}
}

func refused(t *testing.T, err error, code string) {
	t.Helper()
	if r, ok := err.(*Refusal); !ok || r.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestRunRefusalsAndSuccess(t *testing.T) {
	good, handoff := &stub{id: "good", mode: ModeHeadless}, &stub{id: "copy", mode: ModeHandoff}
	s := NewSession(false, handoff, good)
	defer s.Close()
	var records []RunRecord
	s.Record = func(r RunRecord) { records = append(records, r) }
	p := Package{Kind: KindPlan, Markdown: "PAYLOAD-TEXT", SHA256: "h1"}
	scope := RunScope{Project: "net", Root: "/synthetic/net", Workspace: "default", Engine: "terraform"}

	_, err := s.Start("a", "h1", "good", DisclosureVersion, scope)
	refused(t, err, RefuseDisabled)
	refused(t, s.Accept(DisclosureVersion), RefuseDisabled)
	st := s.SetEnabled(true)
	if st.DefaultProvider != "good" || !st.Headless || st.Accepted || len(st.Providers) != 2 {
		t.Fatalf("%+v", st)
	}
	s.Put("a", p, nil)
	_, err = s.Start("a", "h1", "good", DisclosureVersion, scope)
	refused(t, err, RefuseDisclosure)
	refused(t, s.Accept("1"), RefuseDisclosure)
	if err := s.Accept(DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	_, err = s.Start("a", "h1", "good", "1", scope)
	refused(t, err, RefuseDisclosure)
	_, err = s.Start("a", "h1", "copy", DisclosureVersion, scope)
	refused(t, err, RefuseProvider)
	refused(t, s.SetDefault("copy"), RefuseProvider)
	_, err = s.Start("a", "h1", "nope", DisclosureVersion, scope)
	refused(t, err, RefuseProvider)
	_, err = s.Start("b", "h1", "good", DisclosureVersion, scope)
	refused(t, err, RefuseNotFound)
	s.Put("a", Package{Kind: KindPlan, Markdown: "OLD", SHA256: "h2"}, func() bool { return false })
	_, err = s.Start("a", "h2", "good", DisclosureVersion, scope)
	refused(t, err, RefuseStale)

	if !s.Settings().Headless {
		t.Fatal("headless not reported")
	}
	run, err := s.Start("a", "h1", "good", DisclosureVersion, scope)
	if err != nil || run.Status != RunRunning {
		t.Fatal(run, err)
	}
	done := wait(t, s)
	if done.Status != RunCompleted || done.Reply != "reply to PAYL" || done.SHA256 != "h1" || done.Kind != KindPlan || good.got[0] != "PAYLOAD-TEXT" {
		t.Fatalf("%+v", done)
	}
	if len(records) != 1 || records[0].Status != RunCompleted || records[0].SHA256 != "h1" || records[0].Provider != "good" || records[0].Project != "net" {
		t.Fatalf("%+v", records)
	}
	// Turning AI off drops the reply, the packages and the acceptance.
	s.SetEnabled(false)
	if _, ok := s.Current(); ok {
		t.Fatal("reply kept after AI was turned off")
	}
	if s.SetEnabled(true).Accepted {
		t.Fatal("acceptance survived AI off")
	}
}

func TestRunStrictLocalBusyAndCancel(t *testing.T) {
	strict := NewSession(true, &stub{id: "good", mode: ModeHeadless})
	strict.SetEnabled(true)
	_ = strict.Accept(DisclosureVersion)
	strict.Put("a", Package{Kind: KindPlan, Markdown: "x", SHA256: "h"}, nil)
	if _, err := strict.Start("a", "h", "good", DisclosureVersion, RunScope{}); err == nil {
		t.Fatal("strict local-only ran an assistant")
	} else {
		refused(t, err, RefuseStrict)
	}
	if st := strict.Settings(); st.Headless || len(st.Providers) != 0 {
		t.Fatalf("strict mode detected providers: %+v", st)
	}

	slow := &stub{id: "good", mode: ModeHeadless, block: true}
	s := NewSession(false, slow)
	var rec RunRecord
	s.Record = func(r RunRecord) { rec = r }
	s.SetEnabled(true)
	_ = s.Accept(DisclosureVersion)
	s.Put("a", Package{Kind: KindDrift, Markdown: "PAYLOAD", SHA256: "h"}, nil)
	if _, err := s.Start("a", "h", "good", DisclosureVersion, RunScope{}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Start("a", "h", "good", DisclosureVersion, RunScope{})
	refused(t, err, RefuseBusy)
	if !s.Cancel() {
		t.Fatal("nothing to cancel")
	}
	if r := wait(t, s); r.Status != RunCancelled || r.Error == nil || r.Error.Code != KindCancelled || r.Reply != "" {
		t.Fatalf("%+v", r)
	}
	if rec.Status != RunCancelled || strings.Contains(rec.Code+rec.Kind+rec.SHA256, "PAYLOAD") {
		t.Fatalf("%+v", rec)
	}
	// Close cancels a running request and waits for it.
	if _, err := s.Start("a", "h", "good", DisclosureVersion, RunScope{}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if r, _ := s.Current(); r.Status != RunCancelled {
		t.Fatalf("%+v", r)
	}
}
