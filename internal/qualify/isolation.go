package qualify

import (
	"encoding/json"
	"fmt"
)

// probeReport is the part of a netprobe report (internal/qualify/netprobe) the level needs.
type probeReport struct {
	Mode   string `json:"mode"`
	Checks []struct {
		Name     string `json:"name"`
		Outcome  string `json:"outcome"`
		Expected string `json:"expected"`
		OK       *bool  `json:"ok"`
	} `json:"checks"`
	Grandchild *probeReport `json:"grandchild"`
}

// IsolationLevel derives the level from probe results, with the same criteria as
// scripts/qualify/verify-mirror.sh (F-0): the control reaches the OD-5 endpoints over IPv4 TCP
// and UDP; both isolated brackets pass every expected check (including loopback, after the
// F0-LB retry) in the process and its grandchild, and fail the system resolver and the home
// read. The mechanism name alone never gives `enforced`. It also reports whether the home
// canary was readable (OD-14), from the first isolated bracket.
func IsolationLevel(mechanism string, control, before, after []byte) (level, reason string, credentialFilesReachable *bool) {
	var c, b, a probeReport
	var reachable *bool
	if json.Unmarshal(before, &b) == nil {
		reachable = homeReadable(b)
	}
	switch mechanism {
	case "", "none":
		return "none", "no isolation mechanism", reachable
	case "proxy":
		return "weak-proxy", "proxy variables only", reachable
	}
	if json.Unmarshal(control, &c) != nil || reachable == nil {
		return "weak-proxy", "probe results missing or unreadable", reachable
	}
	if !controlReached(c) {
		return "weak-proxy", "control did not reach the OD-5 endpoints, so denial proves nothing", reachable
	}
	if err := isolatedOK(b); err != nil {
		return "weak-proxy", "before: " + err.Error(), reachable
	}
	if after == nil {
		return "weak-proxy", "no isolated probe after the run", reachable
	}
	if json.Unmarshal(after, &a) != nil {
		return "weak-proxy", "after: probe result unreadable", reachable
	}
	if err := isolatedOK(a); err != nil {
		return "weak-proxy", "after: " + err.Error(), reachable
	}
	return "enforced", "", reachable
}

func controlReached(c probeReport) bool {
	ok := map[string]bool{}
	for _, k := range c.Checks {
		if k.OK != nil && *k.OK {
			ok[k.Name] = true
		}
	}
	return c.Mode == "control" && ok["ipv4_tcp"] && ok["udp_dns"]
}

func isolatedOK(r probeReport) error {
	if r.Mode != "isolated" {
		return fmt.Errorf("mode %q", r.Mode)
	}
	if r.Grandchild == nil || len(r.Grandchild.Checks) == 0 {
		return fmt.Errorf("no grandchild checks")
	}
	for _, rep := range []probeReport{r, *r.Grandchild} {
		for _, k := range rep.Checks {
			if k.Expected != "" && (k.OK == nil || !*k.OK) {
				return fmt.Errorf("%s: %s, expected %s", k.Name, k.Outcome, k.Expected)
			}
		}
	}
	seen := 0
	for _, k := range r.Checks {
		if k.Name == "system_resolver" || k.Name == "home_read" {
			seen++
			if k.Outcome != "failed" {
				return fmt.Errorf("%s: %s", k.Name, k.Outcome)
			}
		}
	}
	if seen != 2 {
		return fmt.Errorf("system_resolver or home_read check missing")
	}
	return nil
}

func homeReadable(r probeReport) *bool {
	for _, k := range r.Checks {
		if k.Name == "home_read" {
			v := k.Outcome != "failed"
			return &v
		}
	}
	return nil
}
