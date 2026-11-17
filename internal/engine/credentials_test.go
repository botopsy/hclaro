package engine

import (
	"strings"
	"testing"
)

// Only commands that cannot need backend or provider credentials skip the aws-vault wrapper.
func TestCredentialFree(t *testing.T) {
	for args, want := range map[string]bool{
		"version -json": true, "workspace show": true, "fmt -check -no-color": true, "validate -json": true,
		"workspace list": false, "show -json": false, "plan -input=false": false, "apply": false, "init": false,
		"providers": false, "output -json": false, "graph": false, "": false, "workspace": false,
	} {
		if got := CredentialFree(strings.Fields(args)); got != want {
			t.Errorf("%q: %v", args, got)
		}
	}
}

// Only aws-vault's own error prefix classifies as a credentials failure, not a resource name.
func TestAWSVaultClassification(t *testing.T) {
	if Classify("aws-vault: error: exec: Failed to get credentials for prod: EOF") != "credentials" {
		t.Fatal("aws-vault error not classified")
	}
	if Classify(`Error: creating S3 Bucket (aws-vault-backups): BucketAlreadyExists`) == "credentials" {
		t.Fatal("resource name classified as credentials")
	}
}
