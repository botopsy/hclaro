package imports

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hclaro/fixtures"
	"hclaro/internal/state"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func TestParseAddress(t *testing.T) {
	for _, valid := range []string{
		"aws_instance.web",
		"aws_instance.web[0]",
		"aws_instance.web[12]",
		`aws_instance.web["blue.eu"]`,
		`module.network["eu"].aws_subnet.private[3]`,
		"module.a.module.b[0].terraform_data.x",
		"null_resource.with-dash",
	} {
		a, err := ParseAddress(valid)
		if err != nil || a.String() != valid {
			t.Errorf("%s: %v %q", valid, err, a.String())
		}
	}
	for _, invalid := range []string{
		"", "data.aws_ami.ubuntu", "module.network", "aws_instance", "aws_instance.web[01]", "aws_instance.web[-1]",
		`aws_instance.web["a\"b"]`, `aws_instance.web["${x}"]`, `aws_instance.web["%{x}"]`, `aws_instance.web["a\\b"]`,
		"aws_instance.web[0][1]", "aws_instance.web.extra", "-replace=aws_instance.web", "Aws_instance.web",
		"aws_instance.web\n", "module..aws_instance.web", `aws_instance.web[""]`, "aws_instance.web[x]",
		strings.Repeat("a", 1025) + ".b",
	} {
		if _, err := ParseAddress(invalid); err == nil {
			t.Errorf("%q accepted", invalid)
		}
	}
	a, _ := ParseAddress(`module.network["eu"].module.subnets.aws_subnet.private[0]`)
	if a.ModuleKey() != "network.subnets" || a.Resource() != "aws_subnet.private" || a.InRoot() {
		t.Fatalf("%+v", a)
	}
}

func TestBlockEscapesTemplates(t *testing.T) {
	a, _ := ParseAddress(`aws_s3_bucket.logs["eu"]`)
	got := Block(a, `arn:${aws:x}/%{y}"q"\z`, "aws.west")
	want := "import {\n  to = aws_s3_bucket.logs[\"eu\"]\n  id = \"arn:$${aws:x}/%%{y}\\\"q\\\"\\\\z\"\n  provider = aws.west\n}\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// project creates a root module with the given files and returns its resolved path.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func env(t *testing.T, root string) Env {
	t.Helper()
	idx, err := state.Parse(fixtures.State)
	if err != nil {
		t.Fatal(err)
	}
	return Env{Root: root, DataDir: filepath.Join(root, ".terraform"), Engine: "terraform", Snapshot: "ready", Index: idx, SupportsGenerate: true}
}

func codes(p Preview) string {
	var out []string
	for _, i := range p.Issues {
		out = append(out, i.Severity+":"+i.Code)
	}
	return strings.Join(out, ",")
}

const mainTF = `
resource "aws_s3_bucket" "legacy_logs" {
  bucket = "hclaro-legacy-logs"
}

resource "aws_instance" "pool" {
  # count = 3 in a comment does not count
  for_each = toset(["a", "b"])
  tags = {
    count = "not a meta-argument at depth two"
  }
}

resource "aws_instance" "fleet" {
  user_data = <<EOT
for_each = "inside a heredoc"
EOT
  count = 2
}

import {
  to = aws_s3_bucket.already
  id = "x"
}
`

func TestCheckReadyImport(t *testing.T) {
	root := project(t, map[string]string{"main.tf": mainTF})
	p := Check(env(t, root), Request{Address: "aws_s3_bucket.legacy_logs", ID: "hclaro-legacy-logs", File: "imports.tf"})
	if p.Blocked || codes(p) != "" || p.Config != "found" || p.ConfigFile != "main.tf" || p.CanGenerate {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(p.Content, "import {\n  to = aws_s3_bucket.legacy_logs\n  id = \"hclaro-legacy-logs\"\n}") || p.SHA256 != Digest("imports.tf", "", p.Content) {
		t.Fatalf("content %q", p.Content)
	}
	if p.Recipe == nil || p.Recipe.Legacy != "terraform import 'aws_s3_bucket.legacy_logs' 'hclaro-legacy-logs'" {
		t.Fatalf("%+v", p.Recipe)
	}
	if _, err := os.Stat(filepath.Join(root, "imports.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview wrote a file")
	}
}

func TestCheckCollisionsAndConfiguration(t *testing.T) {
	root := project(t, map[string]string{"main.tf": mainTF, "existing.tf": "# user file\n", "imports_generated.tf": ""})
	e := env(t, root)
	for _, c := range []struct {
		req  Request
		want string
	}{
		{Request{Address: "aws_s3_bucket.artifacts", ID: "new-id", File: "a.tf"}, "error:address_in_state,error:config_missing"},
		{Request{Address: "aws_s3_bucket.other", ID: "hclaro-demo-artifacts", File: "a.tf", GenerateConfig: true}, "error:id_in_state"},
		{Request{Address: "aws_s3_bucket.missing", ID: "x-1", File: "a.tf"}, "error:config_missing"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "a.tf", GenerateConfig: true}, "error:generate_with_config"},
		{Request{Address: "aws_s3_bucket.missing[0]", ID: "x-1", File: "a.tf", GenerateConfig: true}, "error:generate_unsupported"},
		{Request{Address: "aws_s3_bucket.missing", ID: "x-1", File: "imports.tf", GenerateConfig: true}, "error:file_exists"},
		{Request{Address: "aws_s3_bucket.already", ID: "x-1", File: "a.tf", GenerateConfig: true}, "error:import_exists"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "existing.tf"}, "error:file_exists"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "../escape.tf"}, "error:invalid_file"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "main_override.tf"}, "error:invalid_file"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "a.tf.json"}, "error:invalid_file"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "token=abc123", File: "a.tf"}, "error:id_credential"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "line\nbreak", File: "a.tf"}, "error:invalid_id"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: " padded", File: "a.tf"}, "error:invalid_id"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "a.tf", Provider: "aws west"}, "error:invalid_provider"},
		{Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "a.tf", Provider: "google"}, "warning:provider_mismatch"},
		{Request{Address: "data.aws_s3_bucket.x", ID: "x-1", File: "a.tf"}, "error:invalid_address"},
		{Request{Address: "aws_instance.pool[0]", ID: "i-1", File: "a.tf"}, "warning:key_type"},
		{Request{Address: "aws_instance.pool", ID: "i-1", File: "a.tf"}, "warning:key_missing"},
		{Request{Address: `aws_instance.fleet["a"]`, ID: "i-1", File: "a.tf"}, "warning:key_type"},
		{Request{Address: "aws_instance.fleet[1]", ID: "i-1", File: "a.tf"}, ""},
		{Request{Address: `aws_instance.pool["a"]`, ID: "i-1", File: "a.tf"}, ""},
		{Request{Address: "aws_s3_bucket.legacy_logs[0]", ID: "i-1", File: "a.tf"}, "warning:key_unexpected"},
	} {
		p := Check(e, c.req)
		if codes(p) != c.want || p.Blocked != strings.HasPrefix(c.want, "error") {
			t.Errorf("%+v: got %q blocked=%v", c.req, codes(p), p.Blocked)
		}
	}
	// Generation for a missing root, unindexed block names a second new file.
	p := Check(e, Request{Address: "aws_s3_bucket.missing", ID: "x-1", File: "new_import.tf", GenerateConfig: true})
	if p.Blocked || p.GeneratedFile != "new_import_generated.tf" || !p.CanGenerate || p.Config != "missing" {
		t.Fatalf("%+v", p)
	}
	if !strings.Contains(strings.Join(p.Recipe.Commands, "\n"), "terraform plan -out=import.tfplan -generate-config-out=new_import_generated.tf") {
		t.Fatalf("%+v", p.Recipe)
	}
	// An unreadable snapshot cannot rule out collisions.
	e.Snapshot = "error"
	if p := Check(e, Request{Address: "aws_s3_bucket.legacy_logs", ID: "x-1", File: "a.tf"}); !strings.Contains(codes(p), "error:state_unverified") {
		t.Fatal(codes(p))
	}
}

func TestCheckModuleConfiguration(t *testing.T) {
	root := project(t, map[string]string{
		"main.tf":                         `module "network" { source = "./modules/network" }`,
		"modules/network/subnets.tf.json": `{"resource":{"aws_subnet":{"private":{"count":2}}}}`,
		".terraform/modules/modules.json": `{"Modules":[{"Key":"","Source":"","Dir":"."},{"Key":"network","Source":"./modules/network","Dir":"modules/network"},{"Key":"outside","Source":"../x","Dir":"../"}]}`,
	})
	e := env(t, root)
	p := Check(e, Request{Address: `module.network["eu"].aws_subnet.private[1]`, ID: "subnet-1", File: "a.tf"})
	if p.Blocked || p.Config != "found" || p.ConfigFile != "modules/network/subnets.tf.json" || codes(p) != "" {
		t.Fatalf("%+v", p)
	}
	if p := Check(e, Request{Address: "module.network.aws_subnet.public", ID: "subnet-1", File: "a.tf", GenerateConfig: true}); codes(p) != "error:generate_unsupported" {
		t.Fatal(codes(p))
	}
	// Module directories outside the project are not read.
	if p := Check(e, Request{Address: "module.outside.aws_subnet.private", ID: "subnet-1", File: "a.tf"}); codes(p) != "warning:config_unverified" || p.Blocked {
		t.Fatal(codes(p))
	}
}

func TestWriteNewNeverOverwrites(t *testing.T) {
	root := project(t, map[string]string{"main.tf": "# keep\n"})
	if err := WriteNew(root, "imports.tf", []byte("import {}\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "imports.tf")); string(b) != "import {}\n" {
		t.Fatalf("%q", b)
	}
	if err := WriteNew(root, "imports.tf", []byte("other")); !errors.Is(err, ErrFileExists) {
		t.Fatal(err)
	}
	if err := WriteNew(root, "main.tf", []byte("other")); !errors.Is(err, ErrFileExists) {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "main.tf")); string(b) != "# keep\n" {
		t.Fatal("existing file changed")
	}
	for _, name := range []string{"../escape.tf", "sub/x.tf", "x.tfvars", ".hidden.tf", ""} {
		if err := WriteNew(root, name, []byte("x")); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestWriteNewRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := project(t, nil)
	outside := t.TempDir()
	// A dangling symlink at the destination would redirect the write outside the project.
	if err := os.Symlink(filepath.Join(outside, "target.tf"), filepath.Join(root, "link.tf")); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(root, "link.tf", []byte("x")); !errors.Is(err, ErrFileExists) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "target.tf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("write followed a symlink")
	}
	// A project root reached through a symlink is refused.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(alias, "x.tf", []byte("x")); !errors.Is(err, ErrUnsafePath) {
		t.Fatal(err)
	}
	if _, _, err := ReadGenerated(root, "link.tf"); err == nil {
		t.Fatal("generated config read through a symlink")
	}
}

func TestRedactGenerated(t *testing.T) {
	got := RedactGenerated(string(fixtures.ImportGenerated))
	if strings.Contains(got, canary) {
		t.Fatalf("canary shown:\n%s", got)
	}
	for _, want := range []string{`bucket        = "hclaro-legacy-logs"`, `replication_token = "[redacted]"`, "# [redacted heredoc line]", "env = \"demo\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if got := RedactGenerated(`  password = null`); got != `  password = null` {
		t.Fatal(got)
	}
	if got := RedactGenerated(`  description = "AKIAIOSFODNN7EXAMPLE"`); strings.Contains(got, "AKIA") {
		t.Fatal(got)
	}
}
