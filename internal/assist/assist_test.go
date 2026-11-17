package assist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hclaro/internal/drift"
	"hclaro/internal/imports"
	"hclaro/internal/plan"
	"hclaro/internal/state"
)

const awsProvider = "registry.terraform.io/hashicorp/aws"

func rc(addr string, actions []string, before, after any, extra map[string]any) map[string]any {
	typ, name, _ := strings.Cut(addr, ".")
	change := map[string]any{"actions": actions, "before": before, "after": after}
	for k, v := range extra {
		change[k] = v
	}
	return map[string]any{"address": addr, "mode": "managed", "type": typ, "name": name, "provider_name": awsProvider, "change": change}
}

func review(t *testing.T, changes, drifted []map[string]any, outputs map[string]any) plan.Review {
	t.Helper()
	doc := map[string]any{"format_version": "1.2", "terraform_version": "1.9.0", "planned_values": map[string]any{}, "resource_changes": changes}
	if drifted != nil {
		doc["resource_drift"] = drifted
	}
	if outputs != nil {
		doc["output_changes"] = outputs
	}
	b, _ := json.Marshal(doc)
	r, err := plan.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func scope() Scope {
	return Scope{Project: "network", Workspace: "staging", Engine: "terraform", EngineVersion: "1.9.0",
		Providers: []string{awsProvider + " 5.31.0"}, Variables: []string{"region", "db_password"},
		Dependencies: []Dependency{{"aws_instance.web", "aws_security_group.web"}}}
}

func build(t *testing.T, f func() (Package, error)) Package {
	t.Helper()
	p, err := f()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Canaries are credential-shaped, so the redactors and the final scan both recognize them.
var canaries = map[string]string{
	"plain attribute": "DO_NOT_LEAK_plain_attr",
	"json attribute":  "canary-inside-json-policy",
	"nested map":      "DO_NOT_LEAK_nested_map",
	"tag":             "ghp_TagCanary0123456789abcdefABCDEFghij",
	"variable":        "DO_NOT_LEAK_variable_default",
	"comment":         "DO_NOT_LEAK_hcl_comment",
	"output":          "DO_NOT_LEAK_root_output",
	"module output":   "DO_NOT_LEAK_module_output",
	"provider":        "AKIAQWERTYUIOPASDFGH",
	"import id":       "DO_NOT_LEAK_import_id",
	"backend":         "canary-backend-bucket",
	"state attribute": "canary-state-attribute",
	"marked":          "canary-marked-sensitive",
	"generated":       "Zq8wX2mK9vL4nR7tY1pB6cJ3",
}

const canaryConfig = `variable "token_ttl" {
  default = "DO_NOT_LEAK_variable_default"
}

# old value: DO_NOT_LEAK_hcl_comment
provider "aws" {
  region     = "eu-west-1"
  access_key = "AKIAQWERTYUIOPASDFGH"
}

terraform {
  backend "s3" {
    bucket = "canary-backend-bucket"
    key    = "a}b"
    region = "eu-west-1"
  }
}

output "conn" {
  value = "DO_NOT_LEAK_module_output"
}
`

func assertNoCanary(t *testing.T, md string) {
	t.Helper()
	for where, c := range canaries {
		if strings.Contains(md, c) {
			t.Errorf("canary from %s leaked:\n%s", where, md)
		}
	}
}

func TestCanariesAreRedacted(t *testing.T) {
	r := review(t, []map[string]any{
		rc("aws_instance.web", []string{"update"},
			map[string]any{"ami": "ami-1", "engine": "old", "user": "app"},
			map[string]any{
				"ami":      "DO_NOT_LEAK_plain_attr",
				"policy":   `{"Statement":[{"Sid":"a","db_password":"canary-inside-json-policy"}]}`,
				"settings": map[string]any{"inner": map[string]any{"k": "DO_NOT_LEAK_nested_map"}},
				"tags":     map[string]any{"Owner": "ghp_TagCanary0123456789abcdefABCDEFghij"},
				"engine":   "canary-marked-sensitive",
				"user":     "Zq8wX2mK9vL4nR7tY1pB6cJ3",
			},
			map[string]any{"after_sensitive": map[string]any{"engine": true}}),
	}, nil, map[string]any{"conn": map[string]any{"actions": []string{"create"}, "before": nil, "after": "DO_NOT_LEAK_root_output"}})
	s := scope()
	s.Config = []Snippet{{File: "main.tf", Text: canaryConfig}}
	p := build(t, func() (Package, error) { return Plan(s, r) })
	assertNoCanary(t, p.Markdown)
	for _, want := range []string{
		`aws_instance.web ami changed: "ami-1" -> "[redacted]"`,
		`aws_instance.web engine changed: "old" -> "[redacted]" (sensitive)`,
		`aws_instance.web user changed: "app" -> "[redacted]"`,
		`aws_instance.web policy added: null -> "[redacted]"`,
		`aws_instance.web tags.Owner added: null -> "[redacted]"`,
		`create conn: null -> "[redacted]"`,
		`# [backend configuration omitted]`,
		`region     = "eu-west-1"`,
		`db_password`,
	} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("missing %q in:\n%s", want, p.Markdown)
		}
	}

	pre := imports.Preview{Address: "aws_s3_bucket.logs", Config: "found", ConfigFile: "main.tf", Issues: []imports.Issue{}}
	ip := build(t, func() (Package, error) {
		return Import(s, imports.Request{Address: "aws_s3_bucket.logs", ID: "DO_NOT_LEAK_import_id", File: "imports.tf"}, pre)
	})
	assertNoCanary(t, ip.Markdown)
	if !strings.Contains(ip.Markdown, "import id: [redacted]") {
		t.Error(ip.Markdown)
	}

	res := state.Resource{Address: "aws_s3_bucket.logs", Mode: "managed", Type: "aws_s3_bucket", Name: "logs", Provider: awsProvider,
		Attributes: map[string]any{"bucket": "canary-state-attribute"}}
	rp := build(t, func() (Package, error) { return Resource(s, res) })
	assertNoCanary(t, rp.Markdown)
}

func TestBlockedWhenScanFindsSecret(t *testing.T) {
	secret := "ghp_ScopeCanary0123456789abcdefABCDEFgh"
	s := scope()
	s.Variables = append(s.Variables, secret)
	_, err := Resource(s, state.Resource{Address: "aws_vpc.main", Mode: "managed", Type: "aws_vpc", Name: "main"})
	var blocked *Blocked
	if !errors.As(err, &blocked) || len(blocked.Fields) != 1 || !strings.HasPrefix(blocked.Fields[0], "scope.variables[") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("blocked error reveals the value")
	}

	// The state redactor keeps random-looking map keys; the final scan refuses them.
	r := review(t, []map[string]any{rc("aws_instance.web", []string{"update"},
		map[string]any{"tags": map[string]any{}}, map[string]any{"tags": map[string]any{"Zq8wX2mK9vL4nR7tY1pB6cJ3": "x"}}, nil)}, nil, nil)
	_, err = Plan(scope(), r)
	if !errors.As(err, &blocked) || blocked.Fields[0] != "changes[0].attributes[0].path" {
		t.Fatalf("%v", err)
	}

	// Absolute local paths. A fixed root: on Windows a temp dir name (CamelCase test name plus
	// random digits between backslashes) looks secret, so the redactor hides it before the path check.
	root := filepath.Join(string(filepath.Separator)+"projects", "network")
	s = scope()
	s.Root = root
	r = review(t, []map[string]any{rc("local_file.out", []string{"create"}, nil, map[string]any{"filename": filepath.Join(root, "out.txt")}, nil)}, nil, nil)
	if _, err = Plan(s, r); !errors.As(err, &blocked) {
		t.Fatalf("local path not blocked: %v", err)
	}
	s.Config = []Snippet{{File: "../outside.tf", Text: ""}}
	if _, err = Resource(s, state.Resource{Address: "aws_vpc.main"}); !errors.As(err, &blocked) || blocked.Fields[0] != "config[0].file" {
		t.Fatalf("%v", err)
	}
}

func TestPlanScenarios(t *testing.T) {
	changes := []map[string]any{
		rc("aws_db_instance.main", []string{"delete"}, map[string]any{"engine": "postgres"}, nil, nil),
		rc("aws_instance.web", []string{"delete", "create"}, map[string]any{"ami": "ami-1"}, map[string]any{"ami": "ami-2"},
			map[string]any{"replace_paths": [][]any{{"ami"}}}),
		rc("aws_eip.ip", []string{"create"}, nil, map[string]any{"domain": "vpc"}, map[string]any{"after_unknown": map[string]any{"public_ip": true}}),
		rc("aws_vpc.main", []string{"no-op"}, map[string]any{"cidr": "10.0.0.0/16"}, map[string]any{"cidr": "10.0.0.0/16"}, nil),
	}
	for i := 0; i < 40; i++ {
		changes = append(changes, rc("aws_subnet.s"+strings.Repeat("x", i), []string{"create"}, nil, map[string]any{"cidr": "10.0.1.0/24"}, nil))
	}
	changes[0]["action_reason"] = "delete_because_no_resource_config"
	changes[1]["action_reason"] = "replace_because_cannot_update"
	p := build(t, func() (Package, error) { return Plan(scope(), review(t, changes, nil, nil)) })
	for _, want := range []string{
		"delete aws_db_instance.main type=aws_db_instance provider=" + awsProvider + " reason=delete_because_no_resource_config",
		"replace aws_instance.web type=aws_instance",
		`aws_instance.web ami changed: "ami-1" -> "ami-2" (forces replacement)`,
		`aws_eip.ip public_ip unknown: null -> "[known after apply]"`,
		"create: 41", "delete: 1", "replace: 1", "no-op: 1",
	} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(p.Markdown, "no-op aws_vpc.main") {
		t.Error("no-op change listed")
	}
	want := []string{"context", "summary", "changes", "warnings", "attributes", "outputs", "variables", "dependencies"}
	if strings.Join(p.Sections, ",") != strings.Join(want, ",") || len(p.Dropped) != 0 {
		t.Fatalf("%v %v", p.Sections, p.Dropped)
	}
}

func TestDriftOnSensitiveAttribute(t *testing.T) {
	r := review(t, nil, []map[string]any{rc("aws_db_instance.main", []string{"update"},
		map[string]any{"endpoint": "canary-before-endpoint", "size": "small"},
		map[string]any{"endpoint": "canary-after-endpoint", "size": "large"},
		map[string]any{"before_sensitive": map[string]any{"endpoint": true}, "after_sensitive": map[string]any{"endpoint": true}})}, nil)
	res, err := drift.FromReview(r)
	if err != nil {
		t.Fatal(err)
	}
	p := build(t, func() (Package, error) { return Drift(scope(), res) })
	if strings.Contains(p.Markdown, "canary-before-endpoint") || strings.Contains(p.Markdown, "canary-after-endpoint") {
		t.Fatal(p.Markdown)
	}
	for _, want := range []string{
		"changed aws_db_instance.main type=aws_db_instance",
		`aws_db_instance.main endpoint changed: recorded "[redacted]", observed "[redacted]" (sensitive)`,
		`aws_db_instance.main size changed: recorded "small", observed "large"`,
		"changed: 1",
	} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("missing %q in\n%s", want, p.Markdown)
		}
	}
}

func TestImportMissingConfigAndAmbiguousID(t *testing.T) {
	check := func(main string, req imports.Request) (imports.Preview, Scope) {
		root := t.TempDir()
		if main != "" {
			os.WriteFile(filepath.Join(root, "main.tf"), []byte(main), 0o600)
		}
		s := scope()
		s.Root = root
		return imports.Check(imports.Env{Root: root, Snapshot: "empty", Index: state.Empty(), Engine: "terraform"}, req), s
	}
	pre, s := check("", imports.Request{Address: "aws_s3_bucket.logs", ID: "logs", File: "imports.tf"})
	p := build(t, func() (Package, error) {
		return Import(s, imports.Request{Address: "aws_s3_bucket.logs", ID: "logs", File: "imports.tf"}, pre)
	})
	for _, want := range []string{"resource configuration: missing", "error config_missing:", "blocked by Hclaro checks: true", "import id: logs"} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("missing %q in\n%s", want, p.Markdown)
		}
	}
	// The ID names no instance: the block uses count, so the address is ambiguous.
	req := imports.Request{Address: "aws_s3_bucket.logs", ID: "logs", File: "imports.tf"}
	pre, s = check("resource \"aws_s3_bucket\" \"logs\" {\n  count = 2\n}\n", req)
	p = build(t, func() (Package, error) { return Import(s, req, pre) })
	for _, want := range []string{"resource configuration: found", "configuration file: main.tf", "warning key_missing:"} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("missing %q in\n%s", want, p.Markdown)
		}
	}
}

func TestDeterministic(t *testing.T) {
	changes := []map[string]any{
		rc("aws_b.two", []string{"update"}, map[string]any{"m": map[string]any{"z": "1", "a": "2"}}, map[string]any{"m": map[string]any{"z": "3", "a": "4"}}, nil),
		rc("aws_a.one", []string{"create"}, nil, map[string]any{"x": "y"}, nil),
	}
	s := scope()
	s.Config = []Snippet{{File: "main.tf", Text: "resource \"aws_a\" \"one\" {}\n"}}
	p1 := build(t, func() (Package, error) { return Plan(s, review(t, changes, nil, nil)) })
	changes[0], changes[1] = changes[1], changes[0]
	s.Variables = []string{s.Variables[1], s.Variables[0]}
	p2 := build(t, func() (Package, error) { return Plan(s, review(t, changes, nil, nil)) })
	if sum := sha256.Sum256([]byte(p1.Markdown)); hex.EncodeToString(sum[:]) != p1.SHA256 {
		t.Fatal("hash does not match the returned Markdown")
	}
	if p1.Markdown != p2.Markdown || p1.SHA256 != p2.SHA256 {
		t.Fatalf("not deterministic:\n%s\n---\n%s", p1.Markdown, p2.Markdown)
	}
}

func TestBudgetDropsInOrder(t *testing.T) {
	big := strings.Repeat("resource \"aws_vpc\" \"x\" {}\n", 6000)
	s := scope()
	s.Config = []Snippet{{File: "main.tf", Text: big}}
	p := build(t, func() (Package, error) { return Resource(s, state.Resource{Address: "aws_vpc.x"}) })
	if strings.Join(p.Dropped, ",") != "configuration" || !strings.Contains(p.Markdown, "Sections omitted for size: Configuration excerpts\n") || len(p.Markdown) > MaxBytes {
		t.Fatalf("%v", p.Dropped)
	}
	for i := 0; i < 9000; i++ {
		s.Variables = append(s.Variables, "variable_name_number_"+strings.Repeat("v", i%10))
	}
	p = build(t, func() (Package, error) { return Resource(s, state.Resource{Address: "aws_vpc.x"}) })
	if strings.Join(p.Dropped, ",") != "configuration,dependencies,variables" || strings.Contains(p.Markdown, "## Variables") {
		t.Fatalf("%v", p.Dropped)
	}

	var changes []map[string]any
	for i := 0; i < 3000; i++ {
		changes = append(changes, rc("aws_subnet.s"+strings.Repeat("x", i%50)+string(rune('a'+i%26))+strings.Repeat("y", i/26), []string{"create"}, nil, map[string]any{"c": "d"}, nil))
	}
	if _, err := Plan(scope(), review(t, changes, nil, nil)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("%v", err)
	}
}

// headings returns the headings outside fenced blocks, checking that every fence closes.
func headings(t *testing.T, md string) []string {
	t.Helper()
	var out []string
	fence := ""
	for _, line := range strings.Split(md, "\n") {
		switch {
		case fence != "":
			if line == fence {
				fence = ""
			}
		case strings.HasPrefix(line, "```"):
			fence = strings.TrimRight(line, "abcdefghijklmnopqrstuvwxyz")
		case strings.HasPrefix(line, "#"):
			out = append(out, line)
		}
	}
	if fence != "" {
		t.Fatal("unclosed fence")
	}
	return out
}

func TestHostileStringsKeepStructure(t *testing.T) {
	hostile := "x\n## Injected heading\n```\n# also\x1b[31m‮"
	s := scope()
	s.Project = hostile
	s.Dependencies = []Dependency{{"````", "## not a heading"}}
	s.Config = []Snippet{{File: "main.tf", Text: "# ok\n````\n## Fake\n```\n" + strings.Repeat("a", 5000) + "\n\x00\x07 tail\n"}}
	r := review(t, []map[string]any{rc("aws_instance.web", []string{"update"},
		map[string]any{"desc": "a"}, map[string]any{"desc": "```\n# Heading\n" + strings.Repeat("b", 3000)}, nil)}, nil, nil)
	p := build(t, func() (Package, error) { return Plan(s, r) })
	got := headings(t, p.Markdown)
	want := []string{"# Hclaro context: plan", "## Context", "## Summary", "## Resource changes", "## Warnings", "## Attribute changes",
		"## Output changes", "## Variables (names only)", "## Dependencies", "## Configuration excerpts"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("structure changed: %q\n%s", got, p.Markdown)
	}
	for _, line := range strings.Split(p.Markdown, "\n") {
		if len(line) > maxValue+256 {
			t.Fatalf("line of %d bytes", len(line))
		}
	}
	if strings.ContainsAny(p.Markdown, "\x00\x07\x1b‮") || !strings.Contains(p.Markdown, "more bytes not shown]") {
		t.Fatal(p.Markdown)
	}
}

func TestLastKey(t *testing.T) {
	for path, want := range map[string]string{`tags["Owner"]`: "Owner", "ingress[0].cidr_blocks[1]": "cidr_blocks", "policy": "policy", "(value)": "(value)"} {
		if got := lastKey(path); got != want {
			t.Errorf("%s: %q", path, got)
		}
	}
}

func blockedFields(t *testing.T, s Scope) []string {
	t.Helper()
	_, err := Resource(s, state.Resource{Address: "aws_vpc.x"})
	var b *Blocked
	if !errors.As(err, &b) {
		t.Fatalf("not blocked: %v", err)
	}
	return b.Fields
}

// Backend values must not leave the machine in any layout: same-line, one-line and
// template-brace forms are omitted, a JSON backend blocks the excerpt.
func TestBackendLayouts(t *testing.T) {
	for _, text := range []string{
		`terraform { backend "s3" { bucket = "canary-backend-bucket" } }`,
		"terraform { backend \"s3\" {\n  bucket = \"canary-backend-bucket\"\n}}",
		"terraform {\n  backend \"s3\" {\n    key = \"${\"}\"}\"\n    bucket = \"canary-backend-bucket\"\n  }\n}",
		"terraform {\n  cloud {\n    organization = \"canary-backend-bucket\"\n  }\n}",
	} {
		s := scope()
		s.Config = []Snippet{{File: "main.tf", Text: text + "\nlocals { kept = 1 }"}}
		p := build(t, func() (Package, error) { return Resource(s, state.Resource{Address: "aws_vpc.x"}) })
		if strings.Contains(p.Markdown, "canary-backend-bucket") || !strings.Contains(p.Markdown, "kept = 1") {
			t.Errorf("%q:\n%s", text, p.Markdown)
		}
	}
	s := scope()
	s.Config = []Snippet{{File: "main.tf.json", Text: `{"terraform": {"backend": {"s3": {"bucket": "canary-backend-bucket"}}}}`}}
	if f := blockedFields(t, s); f[0] != "config[0].backend" {
		t.Fatal(f)
	}
}

func TestLocalPathVariants(t *testing.T) {
	for _, v := range []string{`C:\Users\Example\Proj\x`, `C:\\Users\\Example\\Proj`, "c:/users/example/proj/x", "C://Users//Example/Proj"} {
		s := scope()
		s.Root = `C:\Users\Example\Proj\`
		s.Providers = []string{v}
		if f := blockedFields(t, s); f[0] != "scope.providers[0]" {
			t.Errorf("%s: %v", v, f)
		}
	}
}

// Callers may pass only configuration files and bare variable names.
func TestExcludedInputsBlock(t *testing.T) {
	for _, file := range []string{"prod.tfvars", "x.auto.tfvars.json", "terraform.tfstate", ".terraform/environment"} {
		s := scope()
		s.Config = []Snippet{{File: file, Text: `db_host = "db.internal"`}}
		if f := blockedFields(t, s); f[0] != "config[0].file" {
			t.Errorf("%s: %v", file, f)
		}
	}
	s := scope()
	s.Variables = []string{`region = "eu-west-1"`}
	if f := blockedFields(t, s); f[0] != "scope.variables[0]" {
		t.Fatal(f)
	}
	b := newBuilder(KindPlan, scope())
	b.val("x", "", map[string]string{"k": "v"})
	if !b.blocked["x"] {
		t.Fatal("unwalked type shown")
	}
}
