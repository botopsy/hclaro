package discovery

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"hclaro/internal/awscli"
	"hclaro/internal/azcli"
	"hclaro/internal/gcpcli"
	"hclaro/internal/state"
)

// GCP is the Google Cloud provider: it lists project objects with the gcloud CLI, whose
// calls are the read-only argv of internal/gcpcli. runner is gcpcli.Exec in production.
func GCP(runner awscli.Runner) Provider { return gcpProvider{runner} }

type gcpProvider struct{ awscli.Runner }

func (gcpProvider) Name() string { return "gcp" }

// Message is the user-facing text of an error kind for provider ("" is AWS).
func Message(provider, kind string) string {
	switch provider {
	case "gcp":
		return gcpcli.Message(kind)
	case "azure":
		return azcli.Message(kind)
	}
	return awscli.Message(kind)
}

var gcpAccountRE = regexp.MustCompile(`^[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9.-]{1,255}$`)

func (p gcpProvider) Verify(ctx context.Context, pin Pin, environ []string) (Verified, error) {
	out, err := p.Run(ctx, environ, []string{"--version"})
	if err != nil {
		return Verified{}, err
	}
	version, supported := gcpcli.ParseVersion(out)
	if !supported {
		return Verified{}, &awscli.CallError{Kind: awscli.KindUnsupportedCLI}
	}
	project, account, region, err := p.config(ctx, environ)
	if err != nil {
		return Verified{}, err
	}
	if account == "" {
		if account, err = p.activeAccount(ctx, environ); err != nil {
			return Verified{}, err
		}
	}
	if project == "" || account == "" {
		return Verified{}, &awscli.CallError{Kind: awscli.KindNoCredentials}
	}
	out, err = p.Run(ctx, environ, []string{"projects", "describe", project, "--format=" + gcpcli.Formats["projects describe"]})
	if err != nil {
		return Verified{}, err
	}
	var doc struct {
		ProjectID string `json:"projectId"`
		State     string `json:"lifecycleState"`
	}
	if json.Unmarshal(out, &doc) != nil || doc.ProjectID != project {
		return Verified{}, &awscli.CallError{Kind: awscli.KindMalformed}
	}
	if doc.State != "ACTIVE" {
		return Verified{}, &awscli.CallError{Kind: awscli.KindAccessDenied}
	}
	source := "gcloud"
	switch {
	case pin.GCPRegion != "":
		region, source = pin.GCPRegion, "profile"
	case region == "":
		source = "default"
	}
	display, kind := displayPrincipal(account)
	return Verified{Account: project, ARN: display, PrincipalType: kind, Partition: "gcp", Region: region, RegionSource: source, CLIVersion: version}, nil
}

// Account is the project gcloud resolves to now. The configuration is local, so this does not
// contact Google; the listing that follows does and fails if the credentials are not valid.
func (p gcpProvider) Account(ctx context.Context, environ []string, _ string) (string, error) {
	project, _, _, err := p.config(ctx, environ)
	if err == nil && project == "" {
		err = &awscli.CallError{Kind: awscli.KindNoCredentials}
	}
	return project, err
}

// config reads the active project, account and region from `gcloud config list`.
func (p gcpProvider) config(ctx context.Context, environ []string) (project, account, region string, err error) {
	out, err := p.Run(ctx, environ, []string{"config", "list", "--format=" + gcpcli.Formats["config list"]})
	if err != nil {
		return "", "", "", err
	}
	var doc struct {
		Core struct {
			Account string `json:"account"`
			Project string `json:"project"`
		} `json:"core"`
		Compute struct {
			Region string `json:"region"`
		} `json:"compute"`
	}
	if json.Unmarshal(out, &doc) != nil || doc.Core.Project != "" && !gcpcli.ValidProject(doc.Core.Project) || doc.Core.Account != "" && !gcpAccountRE.MatchString(doc.Core.Account) || doc.Compute.Region != "" && !gcpRegion.MatchString(doc.Compute.Region) {
		return "", "", "", &awscli.CallError{Kind: awscli.KindMalformed}
	}
	return doc.Core.Project, doc.Core.Account, doc.Compute.Region, nil
}

func (p gcpProvider) activeAccount(ctx context.Context, environ []string) (string, error) {
	out, err := p.Run(ctx, environ, []string{"auth", "list", "--format=" + gcpcli.Formats["auth list"]})
	if err != nil {
		return "", err
	}
	var accounts []struct{ Account, Status string }
	if json.Unmarshal(out, &accounts) != nil {
		return "", &awscli.CallError{Kind: awscli.KindMalformed}
	}
	for _, a := range accounts {
		if a.Status == "ACTIVE" {
			if !gcpAccountRE.MatchString(a.Account) {
				return "", &awscli.CallError{Kind: awscli.KindMalformed}
			}
			return a.Account, nil
		}
	}
	return "", nil
}

// displayPrincipal shows a service account in full and a person as the first character of
// the name and the domain: an e-mail address is personal data.
func displayPrincipal(account string) (display, kind string) {
	if state.Suspicious(account) {
		return "Principal withheld", "unknown"
	}
	name, domain, _ := strings.Cut(account, "@")
	if strings.HasSuffix(domain, ".gserviceaccount.com") {
		return account, "service-account"
	}
	return name[:1] + "…@" + domain, "user"
}

// ListArgs lists the whole project in one call: gcloud pages by itself, so there is no
// token. --limit is one more than the largest scan limit, so a cut list is detected.
func (gcpProvider) ListArgs(t ResourceType, _ string, _ int, _ string) []string {
	return append(slices.Clone(t.args), "--format="+gcpcli.Formats[strings.Join(t.args, " ")], "--limit", itoa(MaxPerTypeLimit+1), "--quiet")
}

var (
	gcpRegion     = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]{1,2}$`)
	gcpZone       = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]{1,2}-[a-z]$`)
	gcpName       = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)
	gcpBucket     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)
	gcpSQLName    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,96}$`)
	gcpLocation   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	gcpProject    = `[a-z][a-z0-9-]{4,28}[a-z0-9]`
	gcpN          = `[a-z]([-a-z0-9]{0,61}[a-z0-9])?`
	gcpR          = `[a-z]+-[a-z]+[0-9]{1,2}`
	gcpNetworkID  = regexp.MustCompile(`^projects/` + gcpProject + `/global/networks/` + gcpN + `$`)
	gcpSubnetID   = regexp.MustCompile(`^projects/` + gcpProject + `/regions/` + gcpR + `/subnetworks/` + gcpN + `$`)
	gcpFirewallID = regexp.MustCompile(`^projects/` + gcpProject + `/global/firewalls/` + gcpN + `$`)
	gcpInstanceID = regexp.MustCompile(`^projects/` + gcpProject + `/zones/` + gcpR + `-[a-z]/instances/` + gcpN + `$`)
	gcpDiskID     = regexp.MustCompile(`^projects/` + gcpProject + `/zones/` + gcpR + `-[a-z]/disks/` + gcpN + `$`)
	gcpBucketID   = gcpBucket
	gcpSQLID      = regexp.MustCompile(`^projects/` + gcpProject + `/instances/[a-z][a-z0-9-]{0,96}$`)
)

func init() {
	g := func(t ResourceType) ResourceType { t.Provider, t.Global = "gcp", true; return t }
	Types = append(Types,
		g(ResourceType{Type: "google_compute_network", idShape: gcpNetworkID, Service: "compute", Label: "VPC networks", Operation: "compute.networks.list", ImportID: "projects/PROJECT/global/networks/NAME", StateKeys: []string{"id"}, Permissions: "compute.networks.list", args: []string{"compute", "networks", "list"}, parse: parseGCPNetworks}),
		g(ResourceType{Type: "google_compute_subnetwork", idShape: gcpSubnetID, Service: "compute", Label: "Subnetworks", Operation: "compute.subnetworks.list", ImportID: "projects/PROJECT/regions/REGION/subnetworks/NAME", StateKeys: []string{"id"}, Permissions: "compute.subnetworks.list", args: []string{"compute", "networks", "subnets", "list"}, parse: parseGCPSubnetworks}),
		g(ResourceType{Type: "google_compute_firewall", idShape: gcpFirewallID, Service: "compute", Label: "Firewall rules", Operation: "compute.firewalls.list", ImportID: "projects/PROJECT/global/firewalls/NAME", StateKeys: []string{"id"}, Permissions: "compute.firewalls.list", args: []string{"compute", "firewall-rules", "list"}, parse: parseGCPFirewalls}),
		g(ResourceType{Type: "google_compute_instance", idShape: gcpInstanceID, Service: "compute", Label: "Compute Engine instances", Operation: "compute.instances.list", ImportID: "projects/PROJECT/zones/ZONE/instances/NAME", StateKeys: []string{"id"}, Permissions: "compute.instances.list", args: []string{"compute", "instances", "list"}, parse: parseGCPInstances}),
		g(ResourceType{Type: "google_compute_disk", idShape: gcpDiskID, Service: "compute", Label: "Persistent disks (zonal)", Operation: "compute.disks.list", ImportID: "projects/PROJECT/zones/ZONE/disks/NAME", StateKeys: []string{"id"}, Permissions: "compute.disks.list", args: []string{"compute", "disks", "list"}, parse: parseGCPDisks}),
		g(ResourceType{Type: "google_storage_bucket", idShape: gcpBucketID, Service: "storage", Label: "Cloud Storage buckets", Operation: "storage.buckets.list", ImportID: "Bucket name", StateKeys: []string{"id", "name"}, Permissions: "storage.buckets.list", args: []string{"storage", "buckets", "list"}, parse: parseGCPBuckets}),
		g(ResourceType{Type: "google_sql_database_instance", idShape: gcpSQLID, Service: "sqladmin", Label: "Cloud SQL instances", Operation: "cloudsql.instances.list", ImportID: "projects/PROJECT/instances/NAME", StateKeys: []string{"id"}, Permissions: "cloudsql.instances.list", args: []string{"sql", "instances", "list"}, parse: parseGCPSQL}),
	)
}

func gcpLabels(m map[string]string) []awsTag {
	tags := make([]awsTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, awsTag{Key: k, Value: v})
	}
	return tags
}

// last is the last path segment of a resource URL.
func last(url string) string { return url[strings.LastIndex(url, "/")+1:] }

// gcpCandidate builds one candidate; path is the import ID (valid when ok).
func gcpCandidate(ctx parseContext, typ, service, name, region, path string, ok bool, labels map[string]string, details map[string]string) CloudResourceCandidate {
	tags, _, omitted := sanitizeTags(gcpLabels(labels))
	c := CloudResourceCandidate{Type: typ, Service: service, ResourceID: cleanText(name, 64), Region: region, Account: ctx.Account, Name: cleanText(name, maxName), Tags: tags, TagsOmitted: omitted, Details: map[string]string{}}
	for k, v := range details {
		if safeWord.MatchString(v) {
			c.Details[k] = v
		}
	}
	c = finish(c, ctx, path, ok)
	if c.ImportStatus == ImportIDVerified {
		c.CloudID = path
	}
	return c
}

func gcpSelected(ctx parseContext, region string) bool {
	return len(ctx.Selected) == 0 || !gcpRegion.MatchString(region) || ctx.Selected[region]
}

func parseGCPNetworks(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		Name          string `json:"name"`
		RoutingConfig struct {
			Mode string `json:"routingMode"`
		} `json:"routingConfig"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, n := range doc {
		path := "projects/" + ctx.Account + "/global/networks/" + n.Name
		out = append(out, gcpCandidate(ctx, "google_compute_network", "compute", n.Name, "global", path, gcpNetworkID.MatchString(path), nil, map[string]string{"routingMode": n.RoutingConfig.Mode}))
	}
	return out, "", nil
}

func parseGCPSubnetworks(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct{ Name, Region, IPCidrRange, Network string }
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, s := range doc {
		region := last(s.Region)
		if !gcpRegion.MatchString(region) || !gcpSelected(ctx, region) {
			continue
		}
		path := "projects/" + ctx.Account + "/regions/" + region + "/subnetworks/" + s.Name
		c := gcpCandidate(ctx, "google_compute_subnetwork", "compute", s.Name, region, path, gcpSubnetID.MatchString(path), nil, map[string]string{"network": last(s.Network)})
		if cidr.MatchString(s.IPCidrRange) {
			c.Details["cidr"] = s.IPCidrRange
		}
		out = append(out, c)
	}
	return out, "", nil
}

// parseGCPFirewalls reads rule identity only: the allowed protocols and source ranges are not
// in the projection and are never decoded.
func parseGCPFirewalls(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		Name      string `json:"name"`
		Network   string `json:"network"`
		Direction string `json:"direction"`
		Disabled  bool   `json:"disabled"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, f := range doc {
		path := "projects/" + ctx.Account + "/global/firewalls/" + f.Name
		details := map[string]string{"network": last(f.Network), "direction": f.Direction}
		if f.Disabled {
			details["disabled"] = "true"
		}
		out = append(out, gcpCandidate(ctx, "google_compute_firewall", "compute", f.Name, "global", path, gcpFirewallID.MatchString(path), nil, details))
	}
	return out, "", nil
}

// zoneRegion is the region of a zone name (us-central1-a is in us-central1).
func zoneRegion(zone string) string { return zone[:strings.LastIndex(zone, "-")] }

func parseGCPInstances(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		Name        string            `json:"name"`
		Zone        string            `json:"zone"`
		MachineType string            `json:"machineType"`
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, i := range doc {
		zone := last(i.Zone)
		if !gcpZone.MatchString(zone) || !gcpSelected(ctx, zoneRegion(zone)) {
			continue
		}
		path := "projects/" + ctx.Account + "/zones/" + zone + "/instances/" + i.Name
		out = append(out, gcpCandidate(ctx, "google_compute_instance", "compute", i.Name, zoneRegion(zone), path, gcpInstanceID.MatchString(path), i.Labels, map[string]string{"zone": zone, "machineType": last(i.MachineType), "status": i.Status}))
	}
	return out, "", nil
}

// parseGCPDisks reads zonal disks; a regional disk has no zone and is another Terraform type.
func parseGCPDisks(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		Name   string            `json:"name"`
		Zone   string            `json:"zone"`
		SizeGB string            `json:"sizeGb"`
		Type   string            `json:"type"`
		Status string            `json:"status"`
		Labels map[string]string `json:"labels"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, d := range doc {
		zone := last(d.Zone)
		if !gcpZone.MatchString(zone) || !gcpSelected(ctx, zoneRegion(zone)) {
			continue
		}
		path := "projects/" + ctx.Account + "/zones/" + zone + "/disks/" + d.Name
		out = append(out, gcpCandidate(ctx, "google_compute_disk", "compute", d.Name, zoneRegion(zone), path, gcpDiskID.MatchString(path), d.Labels, map[string]string{"zone": zone, "type": last(d.Type), "sizeGb": d.SizeGB, "status": d.Status}))
	}
	return out, "", nil
}

func parseGCPBuckets(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		Name         string            `json:"name"`
		Location     string            `json:"location"`
		StorageURL   string            `json:"storage_url"`
		StorageClass string            `json:"default_storage_class"`
		Labels       map[string]string `json:"labels"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, b := range doc {
		name := b.Name
		if name == "" {
			name = strings.Trim(strings.TrimPrefix(b.StorageURL, "gs://"), "/")
		}
		region := strings.ToLower(b.Location)
		if !gcpLocation.MatchString(region) {
			region = ""
		}
		if !gcpSelected(ctx, region) {
			continue
		}
		valid := gcpBucket.MatchString(name) && !strings.Contains(name, "..")
		c := gcpCandidate(ctx, "google_storage_bucket", "storage", name, region, name, valid, b.Labels, map[string]string{"storageClass": b.StorageClass})
		if c.ImportStatus == ImportIDVerified {
			c.CloudID = "projects/_/buckets/" + name
		}
		if region == "" {
			c.Regions = []string{}
		}
		out = append(out, c)
	}
	return out, "", nil
}

func parseGCPSQL(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		Name            string `json:"name"`
		Region          string `json:"region"`
		DatabaseVersion string `json:"databaseVersion"`
		State           string `json:"state"`
		Settings        struct {
			UserLabels map[string]string `json:"userLabels"`
		} `json:"settings"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, d := range doc {
		if !gcpRegion.MatchString(d.Region) || !gcpSelected(ctx, d.Region) {
			continue
		}
		path := "projects/" + ctx.Account + "/instances/" + d.Name
		out = append(out, gcpCandidate(ctx, "google_sql_database_instance", "sqladmin", d.Name, d.Region, path, gcpSQLID.MatchString(path) && gcpSQLName.MatchString(d.Name), d.Settings.UserLabels, map[string]string{"databaseVersion": d.DatabaseVersion, "state": d.State}))
	}
	return out, "", nil
}
