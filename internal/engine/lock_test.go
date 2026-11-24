package engine

import (
	"strings"
	"testing"
)

func TestParseLockInfo(t *testing.T) {
	terraform := "Error message: operation error DynamoDB: PutItem, ConditionalCheckFailedException\nLock Info:\n  ID:        1234-abcd\n  Path:      s3://bucket/DO_NOT_LEAK_HCLARO_TEST_SECRET\n  Operation: OperationTypeApply\n  Who:       alice@build-7\n  Version:   1.9.8\n  Created:   2026-11-05 09:00:00.123456 +0000 UTC\n  Info:      \n"
	tofu := "│ Lock Info:\n│   ID:        9\n│   Path:      terraform.tfstate\n│   Operation: OperationTypePlan\n│   Who:       bob@laptop\n│   Created:   2026-11-05 10:00:00 +0000 UTC\n"
	want := map[string]*LockInfo{
		terraform: {ID: "1234-abcd", Who: "alice@build-7", Operation: "apply", Created: "2026-11-05 09:00:00.123456 +0000 UTC"},
		tofu:      {ID: "9", Who: "bob@laptop", Operation: "plan", Created: "2026-11-05 10:00:00 +0000 UTC"},
	}
	for text, w := range want {
		got := ParseLockInfo(text)
		if got == nil || *got != *w {
			t.Errorf("got %+v, want %+v", got, w)
		}
		if b := strings.ToLower(text); got != nil && strings.Contains(strings.ToLower(got.ID+got.Who+got.Operation+got.Created), "secret") {
			t.Errorf("path leaked from %q", b)
		}
	}
}

func TestParseLockInfoFallsBack(t *testing.T) {
	for name, text := range map[string]string{
		"no block":     "Error acquiring the state lock",
		"no who":       "Lock Info:\n  ID: 1\n  Operation: OperationTypePlan\n",
		"no operation": "Lock Info:\n  Who: a@b\n",
		"garbled":      "Lock Info:\n \x00\x01 Who\n Operation\n",
		"secret who":   "Lock Info:\n  Operation: OperationTypePlan\n  Who: aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5\n",
		"too long":     "Lock Info:\n  Operation: OperationTypePlan\n  Who: " + strings.Repeat("a", 300) + "\n",
	} {
		if got := ParseLockInfo(text); got != nil {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}
