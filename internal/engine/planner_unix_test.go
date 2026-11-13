//go:build !windows

package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A terminal Ctrl+C goes to the terminal's foreground process group. Plan and apply run in
// their own group, so only Hclaro's single interrupt reaches the engine.
func TestPlanAndApplyRunInTheirOwnProcessGroup(t *testing.T) {
	shortGrace(t)
	c := planCLI(t, "slowstop")
	path := filepath.Join(t.TempDir(), "saved")
	if err := os.WriteFile(path, []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func(context.Context, func([]byte)) error{
		"plan":  func(ctx context.Context, line func([]byte)) error { return c.Plan(ctx, PlanRequest{Out: path}, line) },
		"apply": func(ctx context.Context, line func([]byte)) error { return c.Apply(ctx, ApplyRequest{Path: path}, line) },
	} {
		ctx, cancel := context.WithCancel(context.Background())
		own := false
		_ = run(ctx, func(b []byte) {
			var v struct{ PID int }
			if json.Unmarshal(b, &v) == nil && v.PID > 0 {
				group, err := syscall.Getpgid(v.PID)
				own = err == nil && group == v.PID && group != syscall.Getpgrp()
				cancel()
			}
		})
		cancel()
		if !own {
			t.Fatalf("%s: the engine shares Hclaro's process group", name)
		}
	}
}
