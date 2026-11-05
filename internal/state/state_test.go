package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"hclaro/fixtures"
)

const canary = "DO_NOT_LEAK_HCLARO_TEST_SECRET"

func TestFixtureIndexAndRedaction(t *testing.T) {
	idx, err := Parse(fixtures.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Resources) != 316 || idx.Managed != 315 || idx.Data != 1 || len(idx.Modules) != 8 {
		t.Fatalf("wrong counts: %d %d %d %d", len(idx.Resources), idx.Managed, idx.Data, len(idx.Modules))
	}
	encoded, _ := json.Marshal(idx)
	if strings.Contains(string(encoded), canary) {
		t.Fatal("secret crossed parser boundary")
	}
	var found bool
	for _, r := range idx.Resources {
		if r.Address == `module.compute.module.workers["blue.eu"].aws_instance.node["quote\"key"]` {
			found = true
			if r.Module != `module.compute.module.workers["blue.eu"]` {
				t.Fatal(r.Module)
			}
		}
		if r.Address == "aws_s3_bucket.artifacts" {
			v := r.Attributes.(map[string]any)
			if v["id"] != "hclaro-demo-artifacts" || v["name"] != Redacted || v["marked"] != Redacted {
				t.Fatalf("unexpected redaction: %v", v)
			}
		}
	}
	if !found {
		t.Fatal("canonical quoted address lost")
	}
}
func TestEmptyAndMalformed(t *testing.T) {
	idx, err := Parse(fixtures.Empty)
	if err != nil || len(idx.Resources) != 0 {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`{"format_version":"1.0"}`)); !errors.Is(err, ErrNoState) {
		t.Fatalf("missing state: %v", err)
	}
	for _, input := range []string{`{}`, `null`, `{"format_version":"2.0"}`, `{"format_version":"1.0","planned_values":{}}`, `{"format_version":"1.0"} {}`, `{"format_version":"1.0","values":{"root_module":{"resources":[{"address":"x"}]}}}`} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
func TestConservativeRedaction(t *testing.T) {
	v := map[string]any{"name": "demo", "id": "https://user:password@host", "unknown": canary, "password": 1234, "nested": []any{map[string]any{"name": canary}}, "tokens": []any{canary}, canary: "key", "arn": "safe-but-marked"}
	out := redact(v, map[string]any{"arn": true}, "", 0)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), canary) || strings.Contains(string(b), "1234") || strings.Contains(string(b), "password@host") || strings.Contains(string(b), "safe-but-marked") {
		t.Fatalf("leak: %s", b)
	}
}

func TestSensitivityArrayAndEscapedKeys(t *testing.T) {
	var values, masks any
	_ = json.Unmarshal([]byte(`{"items":[{"id":"normal-looking-private-id"},{"name":"public-name"}],"pass\u0077ord":"private-value"}`), &values)
	_ = json.Unmarshal([]byte(`{"items":[{"id":true},false]}`), &masks)
	b, _ := json.Marshal(redact(values, masks, "", 0))
	if strings.Contains(string(b), "normal-looking-private-id") || strings.Contains(string(b), "private-value") || !strings.Contains(string(b), "public-name") {
		t.Fatal("incorrect nested sensitivity handling")
	}
}

func BenchmarkParseFixture(b *testing.B) {
	for range b.N {
		if _, err := Parse(fixtures.State); err != nil {
			b.Fatal(err)
		}
	}
}
func TestThousandInstances(t *testing.T) {
	resources := make([]string, 1000)
	for i := range resources {
		resources[i] = fmt.Sprintf(`{"address":"example_resource.main[%d]","mode":"managed","type":"example_resource","name":"main","index":%d,"values":{"id":"demo-%d"}}`, i, i, i)
	}
	input := []byte(`{"format_version":"1.0","values":{"root_module":{"resources":[` + strings.Join(resources, ",") + `]}}}`)
	idx, err := Parse(input)
	if err != nil || len(idx.Resources) != 1000 {
		t.Fatalf("count=%d err=%v", len(idx.Resources), err)
	}
	duplicate := strings.Replace(string(input), `main[999]`, `main[0]`, 1)
	if _, err := Parse([]byte(duplicate)); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestLongCanonicalModuleAddress(t *testing.T) {
	module := strings.TrimSuffix(strings.Repeat("module.deep.", 60), ".")
	address := module + `.aws_instance.main["blue.eu"]`
	input := fmt.Sprintf(`{"format_version":"1.0","values":{"root_module":{"child_modules":[{"address":%q,"resources":[{"address":%q,"mode":"managed","type":"aws_instance","name":"main","values":{"id":"public-instance"}}]}]}}}`, module, address)
	idx, err := Parse([]byte(input))
	if err != nil || len(idx.Resources) != 1 || idx.Resources[0].Address != address || idx.Resources[0].Module != module {
		t.Fatal("long canonical address was altered or rejected")
	}
}
func FuzzParse(f *testing.F) {
	f.Add(fixtures.Empty)
	f.Add([]byte(`{"format_version":"1.0"}`))
	f.Fuzz(func(t *testing.T, input []byte) { _, _ = Parse(input) })
}

// Ordinary configuration from real AWS plans must be visible: Terraform's own CLI shows it.
func TestOrdinaryValuesVisible(t *testing.T) {
	cases := map[string]any{
		"appliance_mode_support": "disable",
		"dns_support":            "enable",
		"transit_gateway_default_route_table_association": true,
		"transit_gateway_id":                              "tgw-0123456789abcdef0",
		"vpc_owner_id":                                    "000000000000",
		"arn":                                             "arn:aws:ec2:us-west-2:000000000000:transit-gateway-attachment/tgw-attach-0b0d24d09c1335601",
		"blocked_encryption_types":                        "SSE-C",
		"bucket_key_enabled":                              true,
		"kms_master_key_id":                               "arn:aws:kms:us-west-2:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		"sse_algorithm":                                   "aws:kms",
		"private_ip":                                      "10.0.1.15",
		"certificate_arn":                                 "arn:aws:acm:us-west-2:000000000000:certificate/1234abcd-12ab-34cd-56ef-1234567890ab",
		"region":                                          "asia-east1",
		"endpoint":                                        "https://example.internal:8443/health",
		"description":                                     "Basic tier for the SecurityGroupIngress2024Rule",
		"allocated_storage":                               json.Number("40"),
		"Name":                                            "app-a",
	}
	for key, value := range cases {
		if got := Redact(value, nil, key); got != value {
			t.Errorf("%s=%v redacted as %v", key, value, got)
		}
	}
	tags := Redact(map[string]any{"Name": "app-a", "Environment": "dev"}, nil, "tags").(map[string]any)
	if tags["Name"] != "app-a" || tags["Environment"] != "dev" {
		t.Errorf("tag values hidden: %v", tags)
	}
	ids := Redact([]any{"subnet-0123456789abcdef0", "subnet-0fedcba9876543210"}, nil, "subnet_ids").([]any)
	if ids[0] != "subnet-0123456789abcdef0" {
		t.Errorf("list items hidden: %v", ids)
	}
}

// Secrets a provider did not mark sensitive must stay hidden: by name, by shape, or by randomness.
func TestUnmarkedSecretsHidden(t *testing.T) {
	byKey := map[string]string{
		"master_password": "hunter2", "client_secret": "abc", "auth_token": "abc", "private_key": "abc",
		"primary_connection_string": "Server=db;User Id=sa", "user_data": "#!/bin/sh", "access_key": "abc",
		"kube_config": "apiVersion: v1", "certificate_pem": "abc", "token_ttl": "3600",
	}
	for key, value := range byKey {
		if got := Redact(value, nil, key); got != Redacted {
			t.Errorf("risky key %s revealed %v", key, got)
		}
	}
	byShape := []string{
		"AKIAIOSFODNN7EXAMPLE",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R",
		"glpat-x9Fq2LmP7vR4tK8wZ3nB",
		"postgres://admin:s3cr3t@db.internal:5432/app",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIB",
		"Authorization: Bearer abc.def",
		"x9Fq2LmP7vR4tK8wZ3nB5yH1",
		"d41d8cd98f00b204e9800998ecf8427e", // plain-hex API keys look like digests
		`{"Statement":[{"Condition":{"StringEquals":{"sts:ExternalId":"q8W3eR5tY7uI9oP2aS4dF6gH"}}}]}`,
		canary,
	}
	for _, value := range byShape {
		if got := Redact(value, nil, "value"); got != Redacted {
			t.Errorf("credential-shaped value revealed: %v", got)
		}
	}
	// Engine markers always win, at any depth.
	marked := Redact(map[string]any{"rule": []any{map[string]any{"id": "visible", "bucket_key_enabled": true}}}, map[string]any{"rule": []any{map[string]any{"bucket_key_enabled": true}}}, "")
	b, _ := json.Marshal(marked)
	if !strings.Contains(string(b), `"bucket_key_enabled":"[redacted]"`) || !strings.Contains(string(b), `"id":"visible"`) {
		t.Errorf("nested marker: %s", b)
	}
}

// Hash-like instance keys and region names in addresses must not reject the whole state.
func TestOrdinaryMetadataAccepted(t *testing.T) {
	input := `{"format_version":"1.0","terraform_version":"1.9.8","values":{"root_module":{"resources":[` +
		`{"address":"aws_s3_object.asset[\"d41d8cd98f00b204e9800998ecf8427e\"]","mode":"managed","type":"aws_s3_object","name":"asset","index":"d41d8cd98f00b204e9800998ecf8427e","provider_name":"registry.terraform.io/hashicorp/aws","values":{}},` +
		`{"address":"google_compute_instance.asia_web","mode":"managed","type":"google_compute_instance","name":"asia_web","provider_name":"registry.terraform.io/hashicorp/google","values":{"zone":"asia-east1-a"}}]}}}`
	idx, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Resources) != 2 || idx.Resources[0].Index != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Fatalf("%+v", idx.Resources)
	}
}
