package api

import (
	"encoding/json"
	"testing"

	"hclaro/internal/engine/enginetest"
)

func TestResourceListKeyAttributes(t *testing.T) {
	s := newPlanServer(t, &enginetest.Fake{})
	w := s.do("GET", "/api/v1/resources?type=aws_s3_bucket", "")
	var page struct {
		Resources []map[string]any `json:"resources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Resources) == 0 {
		t.Fatalf("%d %v %s", w.Code, err, w.Body.String())
	}
	row := page.Resources[0]
	if row["keyAttributes"] != "hclaro-demo-artifacts" {
		t.Fatalf("keyAttributes %v", row["keyAttributes"])
	}
	if row["attributes"] != nil {
		t.Fatalf("list row carries attributes: %v", row["attributes"])
	}
	// Types outside the allowlist have no summary field at all.
	w = s.do("GET", "/api/v1/resources?type=aws_caller_identity", "")
	var other struct {
		Resources []map[string]any `json:"resources"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &other)
	for _, r := range other.Resources {
		if _, ok := r["keyAttributes"]; ok {
			t.Fatalf("unknown type has keyAttributes: %v", r)
		}
	}
}
