package fixtures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CompareLarge is a synthetic state with n managed instances spread over modules, for
// comparison performance tests. Every fifth instance differs between env values, every
// tenth has a sensitive attribute and the canary.
func CompareLarge(env string, n int) []byte {
	modules := map[string][]map[string]any{}
	for i := 0; i < n; i++ {
		module := fmt.Sprintf("module.svc_%02d", i%20)
		size := "small"
		if i%5 == 0 && env == "prod" {
			size = "large"
		}
		values := map[string]any{"id": fmt.Sprintf("%s-node-%d", env, i), "size": size, "tags": map[string]any{"Environment": env, "Index": fmt.Sprint(i)}}
		sensitive := map[string]any{}
		if i%10 == 0 {
			values["token_hint"] = Canary
			sensitive["token_hint"] = true
		}
		modules[module] = append(modules[module], map[string]any{
			"address": fmt.Sprintf("%s.null_resource.node[%d]", module, i), "mode": "managed", "type": "null_resource", "name": "node", "index": i,
			"provider_name": "registry.terraform.io/hashicorp/null", "values": values, "sensitive_values": sensitive,
		})
	}
	children := []map[string]any{}
	for i := 0; i < 20; i++ {
		module := fmt.Sprintf("module.svc_%02d", i)
		if rs := modules[module]; len(rs) > 0 {
			children = append(children, map[string]any{"address": module, "resources": rs})
		}
	}
	b, _ := json.Marshal(map[string]any{"format_version": "1.0", "terraform_version": "1.9.8", "values": map[string]any{"root_module": map[string]any{"resources": []any{}, "child_modules": children}}})
	return b
}

// CompareProjects are synthetic roots for comparing workspaces in `make demo-hub`: one
// Terraform stack with dev and prod workspaces, and the same stack as an OpenTofu root whose
// prod is recorded by OpenTofu and whose default workspace has a renamed module.
var CompareProjects = []HubProject{
	{Dir: "stack", Engine: "terraform", Workspaces: []string{"default", "dev", "prod"}, Default: "dev", Initialized: true},
	{Dir: "stack-tofu", Engine: "tofu", Workspaces: []string{"default", "prod"}, Default: "prod", Initialized: true},
}

// WriteCompareProjects writes CompareProjects below dir for the fake engine.
func WriteCompareProjects(dir string) error {
	states := map[string][]byte{
		"stack/default": []byte(`{"format_version":"1.0"}`), "stack/dev": CompareDev, "stack/prod": CompareProd,
		"stack-tofu/default": CompareProdRenamed, "stack-tofu/prod": CompareProdTofu,
	}
	for _, p := range CompareProjects {
		root := filepath.Join(dir, p.Dir)
		if err := os.MkdirAll(filepath.Join(root, ".fake-state"), 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(root, ".terraform"), 0o755); err != nil {
			return err
		}
		ext := ".tf"
		if p.Engine == "tofu" {
			ext = ".tofu"
		}
		if err := os.WriteFile(filepath.Join(root, "main"+ext), []byte("# Synthetic Hclaro comparison fixture. Not real infrastructure.\n"), 0o644); err != nil {
			return err
		}
		list := ""
		for _, ws := range p.Workspaces {
			mark := "  "
			if ws == p.Default {
				mark = "* "
			}
			list += mark + ws + "\n"
			if err := os.WriteFile(filepath.Join(root, ".fake-state", ws+".json"), states[p.Dir+"/"+ws], 0o644); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(root, ".fake-workspaces"), []byte(list), 0o644); err != nil {
			return err
		}
	}
	return nil
}
