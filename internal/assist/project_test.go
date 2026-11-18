package assist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// canaryProject plants a canary everywhere the project package must never read from.
func canaryProject(t *testing.T) (root string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "proj")
	outside := filepath.Join(base, "fakehome")
	write(t, outside, ".aws/credentials", "[default]\naws_secret_access_key = CANARY_SYMLINK_HOME\n")
	write(t, base, "shared/main.tf", "resource \"null_resource\" \"o\" { triggers = { v = \"CANARY_OUTSIDE\" } }\n")

	write(t, root, "main.tf", `terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  backend "s3" {
    bucket = "CANARY_BACKEND"
    key    = "state"
  }
}

module "vpc" {
  source = "terraform-aws-modules/vpc/aws"
}

module "shared" {
  source = "../shared"
}

module "app" {
  source = "./modules/app"
}

resource "aws_s3_bucket" "logs" {
  bucket = "example-logs"
}

data "aws_caller_identity" "me" {}
`)
	write(t, root, "variables.tf", `variable "region" {
  type        = string
  description = "AWS region"
  default     = "us-east-1"
}

variable "db_password" {
  type      = string
  default   = "CANARY_VAR_RISKY"
}

variable "tags" {
  type = map(string)
  default = {
    owner = "CANARY_VAR_MAP"
  }
}

variable "token_note" {
  sensitive = true
  default   = "CANARY_VAR_SENSITIVE"
}

variable "motd" {
  default = <<EOT
CANARY_VAR_HEREDOC
EOT
}

output "bucket" {
  description = "Log bucket"
  value       = aws_s3_bucket.logs.id
}
`)
	write(t, root, "locals.tf", "locals {\n  api_token = \"CANARY_LOCAL\"\n}\n")
	write(t, root, "provider.tf", "provider \"aws\" {\n  region     = var.region\n  access_key = \"AKIACANARY0PROVIDER1\"\n}\n")
	write(t, root, "userdata.tf", "resource \"aws_instance\" \"web\" {\n  user_data = <<EOF\n-----BEGIN PRIVATE KEY-----\nCANARY_HEREDOC\nEOF\n}\n")
	write(t, root, "paths.tf", "resource \"local_file\" \"f\" {\n  filename = \""+filepath.ToSlash(root)+"/out.txt\"\n}\n")
	write(t, root, "modules/app/main.tf", "resource \"aws_sqs_queue\" \"q\" {\n  name = \"jobs\"\n}\n")
	write(t, root, "terraform.tfvars", "db_password = \"CANARY_TFVARS\"\n")
	write(t, root, "prod.auto.tfvars", "x = \"CANARY_AUTO_TFVARS\"\n")
	write(t, root, "terraform.tfstate", `{"resources":[{"instances":[{"attributes":{"p":"CANARY_STATE"}}]}]}`)
	write(t, root, "plan.tfplan", "CANARY_SAVED_PLAN")
	write(t, root, ".terraform/modules/x/main.tf", "# CANARY_DOT_TERRAFORM\n")
	write(t, root, ".terraform.lock.hcl", "provider \"registry.terraform.io/hashicorp/aws\" {\n  version     = \"5.31.0\"\n  constraints = \"~> 5.0\"\n  hashes = [\"h1:abc\"]\n}\n")
	write(t, root, "ignored.tf", "# CANARY_GIT_IGNORED\n")
	write(t, root, "build/gen.tf", "# CANARY_GIT_IGNORED_DIR\n")
	write(t, root, "oneline.tf", "variable \"inline\" { sensitive = true, default = \"CANARY_ONE_LINE_DEFAULT\" }\n")
	write(t, root, "nested.tf.json", `{"locals":{"credentials":{"u":"bob","k":"CANARY_JSON_NESTED"}}}`)
	write(t, root, "json.tf.json", `{"terraform":{"backend":{"s3":{"bucket":"CANARY_JSON_BACKEND"}}},"variable":{"db_secret":{"default":"CANARY_JSON_VAR"},"size":{"default":3}},"resource":{"aws_sqs_queue":{"dlq":{"name":"dlq"}}}}`)
	if err := os.Symlink(filepath.Join(outside, ".aws", "credentials"), filepath.Join(root, "creds.tf")); err != nil {
		t.Skip("symbolic links are not available here")
	}
	if err := os.Symlink(filepath.Join(outside, ".aws"), filepath.Join(root, "aws")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestProjectPackageNeverLeaksCanaries(t *testing.T) {
	root := canaryProject(t)
	git := repoListing(t, root, "ignored.tf", "build/gen.tf")
	p, err := Project(Scope{Project: "net", Workspace: "default", Engine: "terraform", Root: root}, git)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindProject || p.SHA256 != hashOf(p.Markdown) {
		t.Fatalf("kind %q, hash mismatch %v", p.Kind, p.SHA256 != hashOf(p.Markdown))
	}
	if strings.Contains(p.Markdown, "CANARY") || strings.Contains(p.Markdown, "AKIA") || strings.Contains(p.Markdown, "PRIVATE KEY") {
		t.Fatalf("a canary reached the package:\n%s", p.Markdown)
	}
	if strings.Contains(strings.ToLower(p.Markdown), strings.ToLower(filepath.ToSlash(root))) {
		t.Fatal("the project root reached the package")
	}
	for _, want := range []string{
		"locals.tf: may contain a secret or a local path",
		"provider.tf: may contain a secret or a local path",
		"userdata.tf: may contain a secret or a local path",
		"paths.tf: may contain a secret or a local path",
		"creds.tf: symbolic link, never followed",
		"configuration files Git ignores or does not list (not read, not named): 2",
		"nested.tf.json: may contain a secret or a local path",
		"var.inline default=<default omitted>",
		// Structure.
		"calls module.vpc source=terraform-aws-modules/vpc/aws (remote, listed only and never fetched)",
		"calls module.shared source=../shared (local, outside the project root and not included)",
		"calls module.app source=./modules/app (local)",
		". (root module): aws_s3_bucket.logs",
		". (root module): data.aws_caller_identity.me",
		"modules/app: aws_sqs_queue.q",
		". (root module): aws_sqs_queue.dlq",
		"required in .: hashicorp/aws",
		"locked: registry.terraform.io/hashicorp/aws version 5.31.0",
		"var.region type=string default shown in file — AWS region",
		"var.db_password type=string default=<default omitted>",
		"var.tags type=map(string) default=<default omitted>",
		"var.token_note default=<default omitted> (sensitive)",
		"var.motd default=<default omitted>",
		"var.db_secret default=<default omitted>",
		"output.bucket — Log bucket",
		"# [backend configuration omitted]",
		`default     = "us-east-1"`,
		neverIncluded,
	} {
		if !strings.Contains(p.Markdown, want) {
			t.Errorf("package lacks %q", want)
		}
	}
	// Deterministic: the same input gives the same bytes and hash.
	if again, err := Project(Scope{Project: "net", Workspace: "default", Engine: "terraform", Root: root}, git); err != nil || again.SHA256 != p.SHA256 || again.Fingerprint != p.Fingerprint {
		t.Error("same input gave a different package")
	}
	if fp, err := ProjectFingerprint(root, git); err != nil || fp != p.Fingerprint || fp == "" {
		t.Errorf("fingerprint %q differs from the package's %q (%v)", fp, p.Fingerprint, err)
	}
	if strings.Contains(p.Markdown, "ignored.tf") || strings.Contains(p.Markdown, "build/gen.tf") {
		t.Error("a file Git ignores was named")
	}
	if t.Failed() {
		t.Log(p.Markdown)
	}
}

// repoListing is a Git listing of every configuration file under root except the ignored ones,
// as gitctx.ConfigFiles reports it for a repository.
func repoListing(t *testing.T, root string, ignored ...string) GitFiles {
	t.Helper()
	listed := map[string]bool{}
	_ = filepath.WalkDir(root, func(abs string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, abs)
		listed[filepath.ToSlash(rel)] = true
		return nil
	})
	for _, i := range ignored {
		delete(listed, i)
	}
	return GitFiles{Repo: true, Listed: listed, Known: true}
}

var noRepo = GitFiles{Known: true}

// Fail closed whenever Hclaro cannot tell which files Git ignores.
func TestProjectBlockedWhenGitIgnoreIsUnknown(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.tf", "resource \"null_resource\" \"a\" {}\n")
	blockedOn := func(git GitFiles, field string) {
		t.Helper()
		_, err := Project(Scope{Root: root}, git)
		var blocked *Blocked
		if !errors.As(err, &blocked) || blocked.Fields[0] != field {
			t.Fatalf("want %s, got %v", field, err)
		}
		if _, err := ProjectFingerprint(root, git); err == nil {
			t.Fatal("a fingerprint was computed for a blocked project")
		}
	}
	// In a repository, Git missing, failing or truncated.
	blockedOn(GitFiles{Repo: true, Known: false}, "project.gitFiles")
	// A root reached through a symbolic link is read like the real folder.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err == nil {
		if p, err := Project(Scope{Root: link}, noRepo); err != nil || !strings.Contains(p.Markdown, `resource "null_resource" "a"`) {
			t.Fatalf("symlinked root: %v\n%s", err, p.Markdown)
		}
	}
	// In a repository, a file Git does not list is never read: an empty listing shares nothing.
	p, err := Project(Scope{Root: root}, GitFiles{Repo: true, Listed: map[string]bool{}, Known: true})
	if err != nil || strings.Contains(p.Markdown, "null_resource") {
		t.Fatalf("unlisted file shared: %v\n%s", err, p.Markdown)
	}
	// Outside a repository, a .gitignore means files the user wanted private: Hclaro cannot
	// apply it, so it refuses, also in a module folder.
	write(t, root, "modules/a/.gitignore", "secret.tf\n")
	blockedOn(noRepo, "project.gitignoreOutsideRepository")
}

// The fingerprint changes with every change to the configuration the package is built from,
// and with nothing else.
func TestProjectFingerprintScope(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.tf", "resource \"null_resource\" \"a\" {}\n")
	write(t, root, "j.tf.json", `{"resource":{}}`)
	write(t, root, "modules/app/main.tf", "# module\n")
	write(t, root, "private.tf", "# ignored\n")
	write(t, root, ".terraform.lock.hcl", "# lock\n")
	fp := func(git GitFiles) string {
		t.Helper()
		f, err := ProjectFingerprint(root, git)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	git := func() GitFiles { return repoListing(t, root, "private.tf") }
	base := fp(git())
	same := func(why string) {
		t.Helper()
		if fp(git()) != base {
			t.Errorf("%s changed the fingerprint", why)
		}
	}
	changed := func(why string, g GitFiles) {
		t.Helper()
		got := fp(g)
		if got == base {
			t.Errorf("%s did not change the fingerprint", why)
		}
		base = got
	}
	write(t, root, "terraform.tfvars", "x = 1\n")
	write(t, root, "README.md", "docs\n")
	write(t, root, "private.tf", "# ignored, edited\n")
	write(t, root, ".terraform/modules/x/main.tf", "# cache\n")
	write(t, filepath.Dir(root), "outside.tf", "# outside\n")
	same("an unrelated or ignored file")
	write(t, root, "main.tf", "resource \"null_resource\" \"b\" {}\n")
	changed("editing a .tf file", git())
	write(t, root, "j.tf.json", `{"resource":{"x":{}}}`)
	changed("editing a .tf.json file", git())
	write(t, root, "modules/app/main.tf", "# module, edited\n")
	changed("editing a local module", git())
	write(t, root, ".terraform.lock.hcl", "# lock, upgraded\n")
	changed("editing the lock file", git())
	if err := os.Rename(filepath.Join(root, "main.tf"), filepath.Join(root, "renamed.tf")); err != nil {
		t.Fatal(err)
	}
	changed("renaming a file", git())
	if err := os.Remove(filepath.Join(root, "renamed.tf")); err != nil {
		t.Fatal(err)
	}
	changed("deleting a file", git())
	changed("a file becoming ignored", repoListing(t, root, "private.tf", "j.tf.json"))
	changed("a file no longer ignored", repoListing(t, root))
}

func TestProjectBudget(t *testing.T) {
	root := t.TempDir()
	// 60 files of 10 resources with 100 tags each: a little over 2 MiB of configuration.
	for i := range 60 {
		var b strings.Builder
		for j := range 10 {
			fmt.Fprintf(&b, "resource \"null_resource\" \"r%02d_%d\" {\n  triggers = {\n", i, j)
			for k := range 100 {
				fmt.Fprintf(&b, "    tag_%03d = \"synthetic budget fixture value %d\"\n", k, k)
			}
			b.WriteString("  }\n}\n")
		}
		write(t, root, fmt.Sprintf("f%02d.tf", i), b.String())
	}
	total := 0
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		info, _ := e.Info()
		total += int(info.Size())
	}
	if total < 2<<20 {
		t.Fatalf("fixture is %d bytes, want at least 2 MiB", total)
	}
	p, err := Project(Scope{Project: "big", Root: root}, noRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Markdown) > MaxBytes {
		t.Fatalf("package is %d bytes", len(p.Markdown))
	}
	if !strings.Contains(p.Markdown, "## Not included (budget)") || !strings.Contains(p.Markdown, "## Configuration files (smallest first)") {
		t.Fatalf("missing budget sections: %v / %v", p.Sections, p.Dropped)
	}
}

// CRLF files and Windows-style separators give the same package as the Unix form.
func TestProjectCRLFMatchesUnix(t *testing.T) {
	files := map[string]string{
		"main.tf":             "variable \"region\" {\n  default = \"us-east-1\"\n}\n\nresource \"null_resource\" \"a\" {}\n",
		"modules/net/main.tf": "output \"id\" {\n  value = \"x\"\n}\n",
	}
	build := func(crlf bool) Package {
		root := t.TempDir()
		for rel, text := range files {
			if crlf {
				text = "\ufeff" + strings.ReplaceAll(text, "\n", "\r\n")
			}
			write(t, root, rel, text)
		}
		p, err := Project(Scope{Project: "net", Root: root}, noRepo)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	unix, windows := build(false), build(true)
	if unix.SHA256 != windows.SHA256 {
		t.Fatalf("CRLF changed the package:\n%s\n---\n%s", unix.Markdown, windows.Markdown)
	}
	if !strings.Contains(unix.Markdown, "# file: modules/net/main.tf") {
		t.Fatal("module file path is not slash-separated")
	}
}
