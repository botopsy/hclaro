// Package costtest provides a recording fake Infracost runner for tests. It never starts a
// process and never contacts a pricing API.
package costtest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"hclaro/fixtures"
	"hclaro/internal/cost"
)

// Runner is a fake cost.Runner. Mode selects the breakdown result: "" (the synthetic
// report), "auth", "pricing", "malformed" or "block" (waits for cancellation).
type Runner struct {
	mu        sync.Mutex
	Installed bool
	Version   string
	Mode      string
	Report    []byte
	calls     [][]string
	inputs    [][]byte
	environs  [][]string
	paths     []string
	modes     []os.FileMode
	// Started is signalled (non-blocking) when a breakdown starts.
	Started chan struct{}
}

// New returns an installed fake reporting Infracost v0.10.39.
func New() *Runner {
	return &Runner{Installed: true, Version: "Infracost v0.10.39\n", Report: fixtures.CostInfracost, Started: make(chan struct{}, 4)}
}

func (r *Runner) Find() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Installed
}

func (r *Runner) Run(ctx context.Context, environ []string, args []string) ([]byte, error) {
	if !cost.Allowed(args) {
		return nil, &cost.ProviderError{Kind: cost.KindRefused}
	}
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(args))
	r.environs = append(r.environs, cost.Environ(environ))
	installed, mode, version, report := r.Installed, r.Mode, r.Version, r.Report
	if len(args) > 1 {
		// The input exists only while the adapter runs; record what it was given.
		b, _ := os.ReadFile(args[2])
		r.inputs = append(r.inputs, b)
		r.paths = append(r.paths, args[2])
		var mode, dirMode os.FileMode
		if info, err := os.Stat(args[2]); err == nil {
			mode = info.Mode().Perm()
		}
		if info, err := os.Stat(filepath.Dir(args[2])); err == nil {
			dirMode = info.Mode().Perm()
		}
		r.modes = append(r.modes, mode, dirMode)
	}
	r.mu.Unlock()
	if !installed {
		return nil, &cost.ProviderError{Kind: cost.KindMissingCLI}
	}
	if len(args) == 1 {
		return []byte(version), nil
	}
	select {
	case r.Started <- struct{}{}:
	default:
	}
	switch mode {
	case "auth":
		return nil, &cost.ProviderError{Kind: cost.Classify("Error: No INFRACOST_API_KEY environment variable is set. Run infracost auth login")}
	case "pricing":
		return nil, &cost.ProviderError{Kind: cost.Classify("Error: Post \"https://pricing.api.infracost.io/graphql\": dial tcp: lookup pricing.api.infracost.io: no such host")}
	case "malformed":
		return []byte("{not json"), nil
	case "block":
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return report, nil
}

// Set changes the fake's behavior.
func (r *Runner) Set(change func(*Runner)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r)
}

// Calls returns every argv the fake received.
func (r *Runner) Calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// Inputs returns the content of each input file at the time it was run.
func (r *Runner) Inputs() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.inputs)
}

// Paths returns the input paths the fake was given.
func (r *Runner) Paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.paths)
}

// Environs returns the adapter environment of each call.
func (r *Runner) Environs() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.environs)
}

// Modes returns the permission bits of each input file and its directory, in pairs.
func (r *Runner) Modes() []os.FileMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.modes)
}
