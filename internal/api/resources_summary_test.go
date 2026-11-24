package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"hclaro/internal/engine/enginetest"
	"hclaro/internal/operations"
	"hclaro/internal/workspace"
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

func TestResourceListGroups(t *testing.T) {
	// 120 synthetic resources: 70 of type "t_a" and 50 of "t_b", interleaved by address.
	var rs []string
	for i := range 120 {
		typ := "t_a"
		if i%12 >= 7 {
			typ = "t_b"
		}
		rs = append(rs, fmt.Sprintf(`{"address":"%[1]s.r%03[2]d","mode":"managed","type":"%[1]s","name":"r%03[2]d","provider_name":"p","values":{}}`, typ, i))
	}
	fake := &enginetest.Fake{VersionValue: "1.9.8", WorkspaceValue: "demo", StateJSON: []byte(`{"format_version":"1.0","values":{"root_module":{"resources":[` + strings.Join(rs, ",") + `]}}}`)}
	store := workspace.New(workspace.Info{Project: "demo", Initialized: true, Backend: "local", ExecutionMode: "local-cli"})
	store.Load(context.Background(), fake, "terraform", nil)
	manager := operations.New(store, fake, t.TempDir(), operations.Deps{})
	t.Cleanup(func() { manager.Close(5 * time.Second) })
	s := &planServer{t, Handler(store, manager, nil, &Services{}, fstest.MapFS{"index.html": {Data: []byte("app")}}, host, session, csrf, "test"), fake, manager, nil}
	type page struct {
		Resources []struct{ Type, Address string }
		Total     int
		Groups    []struct {
			Key   string
			Count int
		}
	}
	get := func(q string) page { return decode[page](t, s.do("GET", "/api/v1/resources?"+q, "")) }
	first, second := get("group=type&limit=100"), get("group=type&limit=100&offset=100")
	for _, p := range []page{first, second} {
		if p.Total != 120 || len(p.Groups) != 2 || p.Groups[0].Key != "t_a" || p.Groups[0].Count != 70 || p.Groups[1].Count != 50 {
			t.Fatalf("%+v", p.Groups)
		}
	}
	// Ordered by group then address: the first 70 rows are t_a, the rest t_b.
	rows := append(first.Resources, second.Resources...)
	for i, r := range rows {
		if want := map[bool]string{true: "t_a", false: "t_b"}[i < 70]; r.Type != want || i > 0 && rows[i-1].Type == r.Type && rows[i-1].Address >= r.Address {
			t.Fatalf("row %d %+v", i, r)
		}
	}
	if w := s.do("GET", "/api/v1/resources?group=provider", ""); w.Code != 400 {
		t.Fatalf("invalid group %d", w.Code)
	}
	if body := s.do("GET", "/api/v1/resources?limit=1", "").Body.String(); strings.Contains(body, `"groups"`) {
		t.Fatalf("no group but groups present: %s", body)
	}
}
