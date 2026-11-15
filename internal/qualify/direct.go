package qualify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Direct engine calls are the complete M27.02 list: setup and read-only verification only.
// Every apply, import, replace, state or workspace mutation goes through Hclaro.
// TestDirectCallsAllowlisted keeps mutating subcommands out of this file.
// The F-2 lock holder (OD-9) and `plan -lock-timeout=0s` are added by the scenarios that
// need them (M27.03 A5, M27.04 L1).
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
