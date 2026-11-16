package qualify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Direct engine calls are the complete M27.02 list: setup and read-only verification only.
// Every apply, import, replace, state or workspace mutation goes through Hclaro.
// TestDirectCallsAllowlisted keeps mutating subcommands out of this file.
// The F-2 lock holders (OD-9) are in holderAllowed; `plan -lock-timeout=0s` is added by
// M27.04 L1.
var directAllowed = map[string]bool{
	"version -json":               true,
	"init -input=false -no-color": true,
	"show -json":                  true,
	"state list":                  true,
	"workspace show":              true,
	"workspace list":              true,
}

// direct runs an allowlisted subcommand of the engine binary in dir with env (checked
// against the allowlist). extra may only be one non-flag argument for `show -json` (a state
// file).
func direct(engine, dir string, env []string, sub string, extra ...string) ([]byte, error) {
	if !directAllowed[sub] {
		return nil, fmt.Errorf("direct engine call %q is not allowed", sub)
	}
	if len(extra) > 1 || len(extra) == 1 && (sub != "show -json" || strings.HasPrefix(extra[0], "-")) {
		return nil, fmt.Errorf("direct engine call %q: extra arguments refused", sub)
	}
	if err := CheckEnvironment(env, nil); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, engine, append(strings.Fields(sub), extra...)...)
	cmd.Dir, cmd.Env = dir, env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s: %v: %s", sub, err, strings.TrimSpace(errb.String()+" "+out.String()))
	}
	return out.Bytes(), nil
}

func parseVersion(out []byte) (string, error) {
	var v struct {
		Terraform string `json:"terraform_version"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.Terraform == "" {
		return "", fmt.Errorf("version -json: unexpected output")
	}
	return v.Terraform, nil
}

// Lock holders (F-2, OD-9) for M27.03 A5 and M27.04 L1: long-running, non-mutating engine
// processes that hold the local-backend state lock. `console` holds it while its stdin is open
// (Terraform 1.5.x, OpenTofu). `plan` (no -out) holds it while a data "local_file" gated by
// TF_VAR_hold blocks on a named pipe (Terraform 1.17.2, whose console takes no lock).
var holderAllowed = map[string]bool{
	"console -no-color":           true,
	"plan -input=false -no-color": true,
}

// Holder is a running lock holder.
type Holder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	gate  gate
	done  chan struct{}
	Log   bytes.Buffer
}

// startHolder starts sub in dir with env (allowlist plus the declared extras). For the plan
// holder, g is the gate the gated data source reads (nil for console).
func startHolder(engine, dir string, env []string, extra map[string]string, sub string, g gate) (*Holder, error) {
	if !holderAllowed[sub] {
		return nil, fmt.Errorf("lock holder %q is not allowed", sub)
	}
	if err := CheckEnvironment(env, extra); err != nil {
		return nil, err
	}
	h := &Holder{cmd: exec.Command(engine, strings.Fields(sub)...), gate: g, done: make(chan struct{})}
	h.cmd.Dir, h.cmd.Env = dir, env
	h.cmd.Stdout, h.cmd.Stderr = &h.Log, &h.Log
	var err error
	if h.stdin, err = h.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if err := h.cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = h.cmd.Wait(); close(h.done) }()
	return h, nil
}

// holderReleaseWait bounds Release; tests shorten it.
var holderReleaseWait = 60 * time.Second

// Close is the deferred cleanup for every exit path: a graceful Release (so the engine removes
// its own lock-info file), with Release's kill only as the last resort. A no-op once exited.
func (h *Holder) Close() error {
	if !h.Alive() {
		return nil
	}
	return h.Release()
}

// Release lets the holder finish (closes stdin; writes the pipe once) and waits up to
// holderReleaseWait, then kills it.
func (h *Holder) Release() error {
	_ = h.stdin.Close()
	if h.gate != nil {
		h.gate.Release()
		defer h.gate.Close()
	}
	select {
	case <-h.done:
		return nil
	case <-time.After(holderReleaseWait):
		_ = h.cmd.Process.Kill()
		<-h.done
		return fmt.Errorf("lock holder did not exit within %s of its release; killed", holderReleaseWait)
	}
}

// Kill stops a holder that was not released (a failed scenario); a no-op once it exited.
func (h *Holder) Kill() {
	if h.Alive() {
		_ = h.cmd.Process.Kill()
		<-h.done
	}
	if h.gate != nil {
		h.gate.Close()
	}
}

// Alive reports whether the holder is still running.
func (h *Holder) Alive() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}
