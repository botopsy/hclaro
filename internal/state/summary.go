package state

import "strings"

// maxSummary bounds a list row's KeyAttributes, in runes.
const maxSummary = 120

// summarySpec lists up to three attribute paths ("a.b"; "x|y" takes the first one present) and
// the separator between them.
type summarySpec struct {
	paths []string
	sep   string
}

// summaries is the allowlist of resource types shown with a one-line summary in lists. Types
// not listed get none.
var summaries = map[string]summarySpec{
	"aws_route":               {[]string{"destination_cidr_block|destination_ipv6_cidr_block|destination_prefix_list_id", "gateway_id|nat_gateway_id|transit_gateway_id|vpc_peering_connection_id|egress_only_gateway_id|network_interface_id|vpc_endpoint_id"}, " → "},
	"aws_instance":            {[]string{"instance_type", "availability_zone"}, " · "},
	"aws_s3_bucket":           {[]string{"bucket"}, ""},
	"aws_security_group":      {[]string{"name", "vpc_id"}, " · "},
	"aws_vpc":                 {[]string{"cidr_block"}, ""},
	"aws_subnet":              {[]string{"cidr_block", "availability_zone"}, " · "},
	"aws_iam_role":            {[]string{"name"}, ""},
	"aws_lambda_function":     {[]string{"function_name", "runtime"}, " · "},
	"aws_db_instance":         {[]string{"engine", "instance_class"}, " · "},
	"google_compute_instance": {[]string{"machine_type", "zone"}, " · "},
	"google_storage_bucket":   {[]string{"name", "location"}, " · "},
	"azurerm_resource_group":  {[]string{"name", "location"}, " · "},
	"azurerm_storage_account": {[]string{"name", "location"}, " · "},
}

// Summary builds a resource's list summary from attrs, which must already be redacted (a
// Resource's Attributes are). A redacted value shows as Redacted, never anything else.
func Summary(typ string, attrs any) string {
	spec, ok := summaries[typ]
	if !ok {
		return ""
	}
	var parts []string
	for _, p := range spec.paths {
		for _, alt := range strings.Split(p, "|") {
			if v, ok := scalarAt(attrs, strings.Split(alt, ".")); ok {
				parts = append(parts, v)
				break
			}
		}
	}
	out := strings.Join(parts, spec.sep)
	if r := []rune(out); len(r) > maxSummary {
		out = string(r[:maxSummary-1]) + "…"
	}
	return out
}

// scalarAt returns the non-empty string or number at path.
func scalarAt(v any, path []string) (string, bool) {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		v = m[k]
	}
	switch s := v.(type) {
	case string:
		return s, s != ""
	case interface{ String() string }: // json.Number
		return s.String(), true
	}
	return "", false
}
