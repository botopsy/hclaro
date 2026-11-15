package qualify

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

// SetupInit runs the direct, mirror-only `init` for a scenario that does not test init and
// re-applies the fixture guard to the lock file it wrote.
func (s *Scenario) SetupInit(dirs ProcessDirs, env []string, engine, project string) {
	if _, err := s.Direct(filepath.Join(dirs.Bin, exeName(engine)), project, env, "init -input=false -no-color"); err != nil {
		s.Fatalf("setup init: %v", err)
	}
	s.result.SetupInit = "direct"
	s.Guard(project)
	s.RefreshBaseline(project)
}

// StartLaunch starts `hclaro --path <project> --no-browser --engine <engine>` and waits for
// the startup read to finish.
func (s *Scenario) StartLaunch(dirs ProcessDirs, env []string, engine, project string) *Hclaro {
	h, err := s.StartHclaro(dirs, env, nil, "--path", project, "--no-browser", "--engine", engine)
	if err != nil {
		s.Fatalf("start hclaro: %v", err)
	}
	s.waitReady(h)
	if !s.run.negativeDone {
		if err := h.client.NegativeChecks(); err != nil {
			s.Fatalf("%v", err)
		}
		s.run.negativeDone = true
		s.Observe("negative checks (no token, wrong Origin, no CSRF) refused")
	}
	return h
}

// PlanDetail is the part of GET /api/v1/plans/{id} the harness reads.
type PlanDetail struct {
	ID          string `json:"id"`
	OperationID string `json:"operationId"`
	Kind        string `json:"kind"`
	Destructive bool   `json:"destructive"`
	// GeneratedConfig is the redacted engine-generated configuration of an import plan.
	GeneratedConfig string         `json:"generatedConfig"`
	Engine          string         `json:"engine"`
	EngineVersion   string         `json:"engineVersion"`
	Errored         bool           `json:"errored"`
	Summary         map[string]int `json:"summary"`
	// Imports counts changes that import an object, whatever their action.
	Imports int `json:"imports"`
	Changes []struct {
		ID              string   `json:"id"`
		Importing       bool     `json:"importing"`
		Address         string   `json:"address"`
		PreviousAddress string   `json:"previousAddress"`
		Actions         []string `json:"actions"`
		Action          string   `json:"action"`
		Reason          string   `json:"reason"`
	} `json:"changes"`
	Raw json.RawMessage `json:"-"`
}

// LaunchPlan asks Hclaro for a plan with varFiles, as the plan dialog does, follows the
// operation to its end and reads the plan and every change detail.
func (s *Scenario) LaunchPlan(h *Hclaro, project, engine string, varFiles []string) PlanDetail {
	return s.LaunchPlanWith(h, project, engine, varFiles, nil)
}

// LaunchPlanWith is LaunchPlan with resource IDs selected for -replace (from GET resources).
func (s *Scenario) LaunchPlanWith(h *Hclaro, project, engine string, varFiles, replace []string) PlanDetail {
	return s.launchPlan(h, project, engine, varFiles, replace, "")
}

// LaunchPlanProfile plans with an execution profile of the registered launch project,
// sending the fingerprint of the context summary the plan dialog showed.
func (s *Scenario) LaunchPlanProfile(h *Hclaro, project, engine, profileID string, varFiles []string) PlanDetail {
	return s.launchPlan(h, project, engine, varFiles, nil, profileID)
}

func (s *Scenario) launchPlan(h *Hclaro, project, engine string, varFiles, replace []string, profileID string) PlanDetail {
	if replace == nil {
		replace = []string{}
	}
	var opts struct {
		CanPlan bool   `json:"canPlan"`
		Reason  string `json:"reason"`
		Engine  string `json:"engine"`
		Version string `json:"version"`
		Context *struct {
			Context struct {
				Fingerprint string `json:"fingerprint"`
			} `json:"context"`
		} `json:"context"`
		ProfileError string `json:"profileError"`
		// Hub views: the engine is in scope, the fingerprint at the top level.
		Scope              *OpScope `json:"scope"`
		ContextFingerprint string   `json:"contextFingerprint"`
	}
	path := "plans/options"
	if profileID != "" {
		path += "?profileId=" + profileID
	}
	if r, err := h.client.Do("GET", path, nil, &opts); err != nil || r.Status != 200 {
		s.Fatalf("plans/options: %v %d", err, r.Status)
	}
	if h.view != nil {
		s.assertScope(h, opts.Scope)
	} else {
		s.AssertEngine(project, engine, opts.Engine, opts.Version)
	}
	if !opts.CanPlan {
		s.Fatalf("planning unavailable: %s", opts.Reason)
	}
	body := map[string]any{"acknowledgeBackendAccess": true, "varFiles": varFiles, "replace": replace}
	if profileID != "" {
		if opts.Context == nil || opts.Context.Context.Fingerprint == "" {
			s.Fatalf("profile context unavailable: %s", opts.ProfileError)
		}
		body["profileId"], body["contextFingerprint"] = profileID, opts.Context.Context.Fingerprint
	}
	if h.view != nil {
		body["contextFingerprint"] = opts.ContextFingerprint
	}
	s.Guard(project)
	var op Operation
	r, err := h.client.Do("POST", "plans", body, &op)
	if err != nil || r.Status != 202 {
		s.Fatalf("POST plans: %v %d %s", err, r.Status, r.Code)
	}
	done, err := h.client.Follow("operations/"+op.ID, 15*time.Minute)
	if err != nil {
		s.Fatalf("plan operation: %v", err)
	}
	if done.Status != "succeeded" || done.PlanID == "" {
		code := ""
		if done.Diagnostic != nil {
			code = done.Diagnostic.Code
		}
		s.Fatalf("plan operation ended %q (%s)", done.Status, code)
	}
	if n := s.CheckPlanArtifacts(h); n == 0 {
		s.Observe("no saved-plan directory present after the plan (permissions not checked)")
	}
	var detail PlanDetail
	r, err = h.client.Do("GET", "plans/"+done.PlanID, nil, &detail)
	if err != nil || r.Status != 200 {
		s.Fatalf("GET plan: %v %d", err, r.Status)
	}
	detail.Raw = r.Body
	s.AssertEngine(project, engine, detail.Engine, detail.EngineVersion)
	for _, c := range detail.Changes {
		if r, err := h.client.Do("GET", "plans/"+done.PlanID+"/changes/"+c.ID, nil, nil); err != nil || r.Status != 200 {
			s.Fatalf("change detail %s: %v %d", c.Address, err, r.Status)
		}
	}
	return detail
}

// HubRegister registers project with an explicit engine preference (POST /api/v1/projects),
// inspects it, and asserts Hclaro resolved the expected engine and version. A registration
// without a preference is a harness error.
func (s *Scenario) HubRegister(h *Hclaro, project, preference, expected string) string {
	if preference != "terraform" && preference != "tofu" {
		s.Fatalf("harness error: project %s registered without an explicit engine preference", filepath.Base(project))
	}
	var p struct {
		ID               string `json:"id"`
		EnginePreference string `json:"enginePreference"`
	}
	r, err := h.client.Do("POST", "projects", map[string]string{"path": project, "alias": filepath.Base(project), "engine": preference}, &p)
	if err != nil || r.Status != 201 {
		s.Fatalf("register project: %v %d %s", err, r.Status, r.Code)
	}
	if p.EnginePreference != preference {
		s.Fatalf("registered preference %q, sent %q", p.EnginePreference, preference)
	}
	var in struct {
		Inspection *struct {
			Engine     string `json:"engine"`
			Version    string `json:"version"`
			Diagnostic *struct {
				Code string `json:"code"`
			} `json:"diagnostic"`
		} `json:"inspection"`
	}
	r, err = h.client.Do("POST", "projects/"+p.ID+"/inspect", map[string]any{}, &in)
	if err != nil || r.Status != 200 || in.Inspection == nil {
		s.Fatalf("inspect project: %v %d %s", err, r.Status, r.Code)
	}
	if in.Inspection.Diagnostic != nil {
		s.Observe("inspection diagnostic %s", in.Inspection.Diagnostic.Code)
	}
	s.AssertEngine(project, expected, in.Inspection.Engine, in.Inspection.Version)
	return p.ID
}

// StartHub starts `hclaro --hub --no-browser`, with --experimental-hub-execution only when
// hubFlag is set (M27.05 scenarios).
func (s *Scenario) StartHub(dirs ProcessDirs, env []string, hubFlag bool) *Hclaro {
	args := []string{"--hub", "--no-browser"}
	if hubFlag {
		args = append(args, "--experimental-hub-execution")
	}
	if hubFlag != s.result.HubExecutionFlag {
		s.Fatalf("harness error: hub flag %v differs from the recorded %v", hubFlag, s.result.HubExecutionFlag)
	}
	h, err := s.StartHclaro(dirs, env, nil, args...)
	if err != nil {
		s.Fatalf("start hclaro hub: %v", err)
	}
	return h
}

func (d PlanDetail) String() string {
	return fmt.Sprintf("%d changes %v", len(d.Changes), d.Summary)
}
