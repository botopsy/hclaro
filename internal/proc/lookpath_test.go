package proc

import "testing"

func TestBatchFileRefusedOnlyOnWindows(t *testing.T) {
	for path, want := range map[string]bool{
		`C:\tools\terraform.cmd`:   true,
		`C:\tools\terraform.BAT`:   true,
		`C:\tools\terraform.cmd. `: true,
		`C:\tools\terraform.exe`:   false,
		`C:\tools\cmd\git.exe`:     false,
		`C:\tools\terraform`:       false,
	} {
		if got := batchFile("windows", path); got != want {
			t.Errorf("windows %q: got %v, want %v", path, got, want)
		}
	}
	if batchFile("linux", "/usr/local/bin/terraform.cmd") || batchFile("darwin", "/opt/x.bat") {
		t.Fatal("batch-file refusal applies only on Windows")
	}
}
