package fixtures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Canary is planted in every synthetic Hub state. It must never appear in a response.
const Canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

// HubProject describes one synthetic project written by WriteHubProjects.
type HubProject struct {
	Dir, Engine string
	// Workspaces lists the workspaces; Default is the CLI default.
	Workspaces []string
	Default    string
	// Initialized projects get an empty .terraform data directory.
	Initialized bool
}

// HubProjects are the synthetic roots used by tests and `make demo-hub`: a Terraform
// project with default/dev/prod, an OpenTofu project with default/staging and an
// uninitialized project.
var HubProjects = []HubProject{
	{Dir: "network", Engine: "terraform", Workspaces: []string{"default", "dev", "prod"}, Default: "dev", Initialized: true},
	{Dir: "platform", Engine: "tofu", Workspaces: []string{"default", "staging"}, Default: "default", Initialized: true},
	{Dir: "legacy", Engine: "terraform", Workspaces: []string{"default"}, Default: "default"},
}

// WriteHubProjects writes the synthetic projects below dir for the fake engine
// (scripts/fake-engine), which reads .fake-workspaces and .fake-state/<workspace>.json.
// Each workspace's state has resources named after the workspace, so tests can tell
// snapshots apart, and a planted canary under a secret-like attribute.
func WriteHubProjects(dir string) error {
	for _, p := range HubProjects {
		root := filepath.Join(dir, p.Dir)
		if err := os.MkdirAll(filepath.Join(root, ".fake-state"), 0o755); err != nil {
			return err
		}
		ext := ".tf"
		if p.Engine == "tofu" {
			ext = ".tofu"
		}
		if err := os.WriteFile(filepath.Join(root, "main"+ext), []byte("# Synthetic Hclaro fixture. Not real infrastructure.\nresource \"aws_s3_bucket\" \"logs\" {}\n"), 0o644); err != nil {
			return err
		}
		list := ""
		for _, ws := range p.Workspaces {
			mark := "  "
			if ws == p.Default {
				mark = "* "
			}
			list += mark + ws + "\n"
			if err := os.WriteFile(filepath.Join(root, ".fake-state", ws+".json"), HubState(p.Dir, ws), 0o644); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(root, ".fake-workspaces"), []byte(list), 0o644); err != nil {
			return err
		}
		if p.Initialized {
			if err := os.MkdirAll(filepath.Join(root, ".terraform"), 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

// HubState is the synthetic `show -json` document for one project workspace.
func HubState(project, ws string) []byte {
	resource := func(kind, name string, values map[string]any) map[string]any {
		return map[string]any{"address": kind + "." + name, "mode": "managed", "type": kind, "name": name, "provider_name": "registry.terraform.io/hashicorp/aws", "values": values, "sensitive_values": map[string]any{}}
	}
	resources := []map[string]any{
		resource("aws_s3_bucket", ws+"_logs", map[string]any{"id": fmt.Sprintf("%s-%s-logs", project, ws), "bucket": fmt.Sprintf("%s-%s-logs", project, ws), "tags": map[string]any{"Environment": ws}}),
		resource("aws_db_instance", ws+"_main", map[string]any{"id": fmt.Sprintf("%s-%s-db", project, ws), "password": Canary, "engine": "postgres"}),
	}
	if ws == "prod" {
		resources = append(resources, resource("aws_iam_role", "prod_only", map[string]any{"id": "prod-only-role", "name": "prod-only-role"}))
	}
	b, _ := json.MarshalIndent(map[string]any{"format_version": "1.0", "terraform_version": "1.9.8", "values": map[string]any{"root_module": map[string]any{"resources": resources}}}, "", "  ")
	return b
}
