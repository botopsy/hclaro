package assist

import (
	"time"

	"hclaro/internal/history"
)

// Event converts a finished turn for the operation history. Since AI-01.07 the hash (kept as
// packageSha256 for format compatibility) is that of the turn's exact stdin.
func (r RunRecord) Event() history.Event {
	status := map[string]string{RunCompleted: history.StatusSucceeded, RunFailed: history.StatusFailed, RunCancelled: history.StatusCancelled}[r.Status]
	if status == "" {
		status = history.StatusUnknown
	}
	ev := r.RunScope.event(r.ID, "assist", r.Provider, status, r.StartedAt, r.Duration)
	ev.Assist = &history.AssistSummary{Provider: r.Provider, ContextKind: r.Kind, PackageSHA256: r.SHA256, Prompt: r.Prompt}
	if r.Code != "" {
		ev.Diagnostic = &history.Diagnostic{Code: r.Code, Message: Message(r.Code)}
	}
	return ev
}

// Event converts a proposed change decision for the operation history; history gives it an ID.
func (r ChangeRecord) Event() history.Event {
	ev := r.RunScope.event("", "assist-change", r.Outcome, history.StatusSucceeded, r.At, 0)
	ev.Outcome = r.Outcome
	ev.Assist = &history.AssistSummary{Provider: r.Provider, Conversation: r.Conversation, Path: r.Path, BaseSHA256: r.BaseSHA256, NewSHA256: r.NewSHA256}
	if r.Code != "" {
		ev.Diagnostic = &history.Diagnostic{Code: r.Code, Message: ChangeReason(r.Code)}
	}
	return ev
}

func (s RunScope) event(id, kind, action, status string, started time.Time, d time.Duration) history.Event {
	return history.Event{ID: id, Kind: kind, Action: action, Status: status, Final: true, StartedAt: started.UTC().Format(time.RFC3339),
		FinishedAt: started.Add(d).UTC().Format(time.RFC3339), DurationMs: d.Milliseconds(), Root: s.Root, ProjectName: s.Project,
		Engine: s.Engine, Workspace: s.Workspace, Command: []string{}}
}
