package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hclaro/fixtures"
	"hclaro/internal/history"
	"hclaro/internal/operations"
)

func TestArguments(t *testing.T) {
	o, err := Parse([]string{"--path", "./prod", "--port", "8080", "--engine", "tofu", "--no-browser"}, io.Discard)
	if err != nil || o.Path != "./prod" || o.Port != 8080 || o.Engine != "tofu" || !o.NoBrowser {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"--port", "-1"}, {"--port", "65536"}, {"--engine", "sh"}, {"extra"}, {"--wat"}} {
		if _, err := Parse(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := Parse([]string{"--help"}, io.Discard); err != flag.ErrHelp {
		t.Fatal(err)
	}
	if o, err := Parse([]string{"--version"}, io.Discard); err != nil || !o.Version {
		t.Fatal(err)
	}
}

func TestHubArguments(t *testing.T) {
	o, err := Parse(nil, io.Discard)
	if err != nil || o.Hub || o.PathSet || o.Path != "." {
		t.Fatalf("%+v %v", o, err)
	}
	if o, _ := Parse([]string{"--hub"}, io.Discard); !o.Hub || o.PathSet {
		t.Fatalf("%+v", o)
	}
	if o, _ := Parse([]string{"--hub", "--path", "."}, io.Discard); !o.Hub || !o.PathSet {
		t.Fatalf("%+v", o)
	}
}

func TestExportHistory(t *testing.T) {
	if _, err := Parse([]string{"--export-history", "history.txt"}, io.Discard); err == nil {
		t.Fatal("accepted a .txt export")
	}
	if o, err := Parse([]string{"--export-history", "history.md"}, io.Discard); err != nil || o.ExportHistory != "history.md" {
		t.Fatalf("%+v %v", o, err)
	}
	config := t.TempDir()
	t.Setenv("HCLARO_CONFIG_DIR", config)
	store := history.Open(config, time.Now())
	store.Record(operations.Record{Operation: operations.Operation{ID: strings.Repeat("a", 32), Kind: "plan", Status: operations.Failed, StartedAt: time.Now().UTC().Format(time.RFC3339), Diagnostic: &operations.Diagnostic{Code: "plan_failed", Message: "password=" + fixtures.Canary}}, Root: "/synthetic/net", Engine: "terraform", Final: true})
	out := filepath.Join(t.TempDir(), "history.json")
	var log strings.Builder
	// Export runs no server and reads no project, even when a path is given.
	if err := Run(context.Background(), Options{ExportHistory: out, Path: "/nonexistent"}, &log, "test"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(b), strings.Repeat("a", 32)) || strings.Contains(string(b), fixtures.Canary) || !strings.Contains(log.String(), "Wrote 1 operation(s)") {
		t.Fatalf("%v %s %s", err, b, log.String())
	}
	if err := Run(context.Background(), Options{ExportHistory: out}, io.Discard, "test"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrote an existing export: %v", err)
	}
}

// Hub execution is a hidden, experimental opt-in: off by default and not listed in --help.
func TestHubExecutionIsAHiddenOptIn(t *testing.T) {
	if o, err := Parse([]string{}, io.Discard); err != nil || o.HubExecution {
		t.Fatalf("hub execution by default: %+v %v", o, err)
	}
	if o, err := Parse([]string{"--experimental-hub-execution"}, io.Discard); err != nil || !o.HubExecution {
		t.Fatalf("--experimental-hub-execution: %+v %v", o, err)
	}
	var help strings.Builder
	if _, err := Parse([]string{"-h"}, &help); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	if strings.Contains(help.String(), "hub-execution") || !strings.Contains(help.String(), "-strict-local") || !strings.Contains(help.String(), "-export-history file") {
		t.Fatalf("help:\n%s", help.String())
	}
}

func TestStrictLocalArgument(t *testing.T) {
	if o, err := Parse([]string{}, io.Discard); err != nil || o.StrictLocal {
		t.Fatalf("strict local by default: %+v %v", o, err)
	}
	if o, err := Parse([]string{"--strict-local"}, io.Discard); err != nil || !o.StrictLocal {
		t.Fatalf("--strict-local: %+v %v", o, err)
	}
}
