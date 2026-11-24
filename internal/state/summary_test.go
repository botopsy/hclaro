package state

import (
	"strings"
	"testing"
)

func TestSummary(t *testing.T) {
	parse := func(values, sensitive string) any {
		t.Helper()
		doc := `{"format_version":"1.0","values":{"root_module":{"resources":[{"address":"x.y","mode":"managed","type":"t","name":"y","provider_name":"p","values":` + values + `,"sensitive_values":` + sensitive + `}]}}}`
		idx, err := Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return idx.Resources[0].Attributes
	}
	t.Run("route uses the first present alternative", func(t *testing.T) {
		got := Summary("aws_route", parse(`{"destination_cidr_block":"0.0.0.0/0","gateway_id":null,"nat_gateway_id":"nat-1"}`, `{}`))
		if got != "0.0.0.0/0 → nat-1" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("sensitive-marked value is redacted", func(t *testing.T) {
		got := Summary("aws_instance", parse(`{"instance_type":"CANARY_MARKED","availability_zone":"eu-1a"}`, `{"instance_type":true}`))
		if got != Redacted+" · eu-1a" || strings.Contains(got, "CANARY") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("secret-named key is redacted", func(t *testing.T) {
		got := Summary("aws_db_instance", parse(`{"engine":"postgres","instance_class":"db.t3"}`, `{}`))
		if got != "postgres · db.t3" {
			t.Fatalf("got %q", got)
		}
		if got := Summary("aws_s3_bucket", map[string]any{"bucket": Redacted}); got != Redacted {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("unknown type has none", func(t *testing.T) {
		if got := Summary("aws_unknown", map[string]any{"name": "a"}); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("capped at 120 runes", func(t *testing.T) {
		got := Summary("aws_s3_bucket", map[string]any{"bucket": strings.Repeat("é", 300)})
		if n := len([]rune(got)); n != 120 {
			t.Fatalf("runes %d", n)
		}
	})
}
