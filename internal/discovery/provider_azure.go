package discovery

// Init order: Go runs init in file-name order, and catalog.go assigns Types, so this file must
// sort after it (hence not azure.go).

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"hclaro/internal/awscli"
	"hclaro/internal/azcli"
	"hclaro/internal/state"
)

// Azure is the Microsoft Azure provider: it lists the objects of one subscription with the az
// CLI, whose calls are the read-only argv of internal/azcli. runner is azcli.Exec in production.
func Azure(runner awscli.Runner) Provider { return azureProvider{runner} }

type azureProvider struct{ awscli.Runner }

func (azureProvider) Name() string { return "azure" }

var azureUserRE = regexp.MustCompile(`^[A-Za-z0-9._%+#~-]{1,64}@[A-Za-z0-9.-]{1,255}$`)

// azureIdentity is what `az account show` reports, validated.
type azureIdentity struct {
	subscription, tenant, principal, kind string
}

func (p azureProvider) show(ctx context.Context, environ []string) (azureIdentity, error) {
	out, err := p.Run(ctx, environ, []string{"account", "show", "--query", azcli.Queries["account show"], "-o", "json", "--only-show-errors"})
	if err != nil {
		return azureIdentity{}, err
	}
	var doc struct {
		ID, TenantID, User, UserType, Cloud, State string
	}
	if json.Unmarshal(out, &doc) != nil || !azcli.ValidGUID(doc.ID) || !azcli.ValidGUID(doc.TenantID) {
		return azureIdentity{}, &awscli.CallError{Kind: awscli.KindMalformed}
	}
	if doc.Cloud != "AzureCloud" {
		return azureIdentity{}, &awscli.CallError{Kind: azcli.KindUnsupportedCloud}
	}
	if doc.State != "Enabled" {
		return azureIdentity{}, &awscli.CallError{Kind: awscli.KindAccessDenied}
	}
	principal, kind := azurePrincipal(doc.User, doc.UserType)
	return azureIdentity{strings.ToLower(doc.ID), strings.ToLower(doc.TenantID), principal, kind}, nil
}

func (p azureProvider) Verify(ctx context.Context, pin Pin, environ []string) (Verified, error) {
	out, err := p.Run(ctx, environ, []string{"--version"})
	if err != nil {
		return Verified{}, err
	}
	version, supported := azcli.ParseVersion(out)
	if !supported {
		return Verified{}, &awscli.CallError{Kind: awscli.KindUnsupportedCLI}
	}
	id, err := p.show(ctx, environ)
	if err != nil {
		return Verified{}, err
	}
	// The context's names are expectations: az reporting another subscription or tenant is a
	// mismatch, not a new selection.
	if pin.AzureSubscription != "" && !strings.EqualFold(pin.AzureSubscription, id.subscription) || pin.AzureTenant != "" && !strings.EqualFold(pin.AzureTenant, id.tenant) {
		return Verified{}, &awscli.CallError{Kind: awscli.KindInvalid}
	}
	location, source := pin.AzureLocation, "profile"
	if location == "" {
		location, source = azureDefaultLocation(environ)
	}
	return Verified{Account: id.subscription, ARN: id.principal, PrincipalType: id.kind, Partition: "azure", Region: location, RegionSource: source, CLIVersion: version, Tenant: id.tenant}, nil
}

func azureDefaultLocation(environ []string) (location, source string) {
	for i := len(environ) - 1; i >= 0; i-- {
		if key, v, _ := strings.Cut(environ[i], "="); strings.EqualFold(key, "AZURE_DEFAULTS_LOCATION") && azureLocation.MatchString(v) {
			return v, "azure"
		}
	}
	return "eastus", "default"
}

// Account is the subscription az resolves to now. It reads only the local login state.
func (p azureProvider) Account(ctx context.Context, environ []string, _ string) (string, error) {
	id, err := p.show(ctx, environ)
	return id.subscription, err
}

// azurePrincipal shows a service principal or managed identity in full and a person as the
// first character of the name and the domain: a user principal name is personal data.
func azurePrincipal(user, userType string) (display, kind string) {
	if state.Suspicious(user) {
		return "Principal withheld", "unknown"
	}
	if userType == "servicePrincipal" {
		if !safeWord.MatchString(user) {
			return "Principal withheld", "service-principal"
		}
		return user, "service-principal"
	}
	name, domain, ok := strings.Cut(user, "@")
	if userType != "user" || !ok || !azureUserRE.MatchString(user) {
		return "Principal withheld", "unknown"
	}
	return name[:1] + "…@" + domain, "user"
}

// ListArgs lists the whole subscription in one call: az pages by itself, so there is no
// token. The service cuts a list at the scan limit and marks it truncated.
func (azureProvider) ListArgs(t ResourceType, _ string, _ int, _ string) []string {
	return append(slices.Clone(t.args), "--query", azcli.Queries[strings.Join(t.args, " ")], "-o", "json", "--only-show-errors")
}

var (
	azureLocation = regexp.MustCompile(`^[a-z][a-z0-9]{2,29}$`)
	azureSub      = `(?i)^/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/[A-Za-z0-9_.()-]{1,89}[A-Za-z0-9_()-]`
	azureN        = `[A-Za-z0-9][A-Za-z0-9_.-]{0,79}`
	azureGroupID  = regexp.MustCompile(azureSub + `$`)
	azureVNetID   = regexp.MustCompile(azureSub + `/providers/Microsoft\.Network/virtualNetworks/` + azureN + `$`)
	azureSubnetID = regexp.MustCompile(azureSub + `/providers/Microsoft\.Network/virtualNetworks/` + azureN + `/subnets/` + azureN + `$`)
	azureNSGID    = regexp.MustCompile(azureSub + `/providers/Microsoft\.Network/networkSecurityGroups/` + azureN + `$`)
	azureVMID     = regexp.MustCompile(azureSub + `/providers/Microsoft\.Compute/virtualMachines/` + azureN + `$`)
	azureDiskID   = regexp.MustCompile(azureSub + `/providers/Microsoft\.Compute/disks/` + azureN + `$`)
	azureStoreID  = regexp.MustCompile(azureSub + `/providers/Microsoft\.Storage/storageAccounts/[a-z0-9]{3,24}$`)
)

func init() {
	a := func(t ResourceType) ResourceType { t.Provider, t.Global = "azure", true; return t }
	Types = append(Types,
		a(ResourceType{Type: "azurerm_resource_group", idShape: azureGroupID, Service: "resources", Label: "Resource groups", Operation: "Microsoft.Resources/subscriptions/resourceGroups/read", ImportID: "/subscriptions/SUB/resourceGroups/NAME", StateKeys: []string{"id"}, Permissions: "Microsoft.Resources/subscriptions/resourceGroups/read", args: []string{"group", "list"}, parse: parseAzureGroups}),
		a(ResourceType{Type: "azurerm_virtual_network", idShape: azureVNetID, Service: "network", Label: "Virtual networks", Operation: "Microsoft.Network/virtualNetworks/read", ImportID: "Full resource ID (…/virtualNetworks/NAME)", StateKeys: []string{"id"}, Permissions: "Microsoft.Network/virtualNetworks/read", args: []string{"network", "vnet", "list"}, parse: parseAzureVNets}),
		a(ResourceType{Type: "azurerm_subnet", idShape: azureSubnetID, Service: "network", Label: "Subnets", Operation: "Microsoft.Network/virtualNetworks/subnets/read", ImportID: "Full resource ID (…/virtualNetworks/VNET/subnets/NAME)", StateKeys: []string{"id"}, Permissions: "Microsoft.Network/virtualNetworks/read", args: []string{"network", "vnet", "list"}, parse: parseAzureSubnets}),
		a(ResourceType{Type: "azurerm_network_security_group", idShape: azureNSGID, Service: "network", Label: "Network security groups", Operation: "Microsoft.Network/networkSecurityGroups/read", ImportID: "Full resource ID (…/networkSecurityGroups/NAME)", StateKeys: []string{"id"}, Permissions: "Microsoft.Network/networkSecurityGroups/read", args: []string{"network", "nsg", "list"}, parse: parseAzureNSGs}),
		a(ResourceType{Type: "azurerm_linux_virtual_machine", idShape: azureVMID, Service: "compute", Label: "Linux virtual machines", Operation: "Microsoft.Compute/virtualMachines/read", ImportID: "Full resource ID (…/virtualMachines/NAME)", StateKeys: []string{"id"}, Permissions: "Microsoft.Compute/virtualMachines/read", args: []string{"vm", "list"}, parse: azureVMs("Linux", "azurerm_linux_virtual_machine")}),
		a(ResourceType{Type: "azurerm_windows_virtual_machine", idShape: azureVMID, Service: "compute", Label: "Windows virtual machines", Operation: "Microsoft.Compute/virtualMachines/read", ImportID: "Full resource ID (…/virtualMachines/NAME)", StateKeys: []string{"id"}, Permissions: "Microsoft.Compute/virtualMachines/read", args: []string{"vm", "list"}, parse: azureVMs("Windows", "azurerm_windows_virtual_machine")}),
		a(ResourceType{Type: "azurerm_managed_disk", idShape: azureDiskID, Service: "compute", Label: "Managed disks", Operation: "Microsoft.Compute/disks/read", ImportID: "Full resource ID (…/disks/NAME)", StateKeys: []string{"id"}, Permissions: "Microsoft.Compute/disks/read", args: []string{"disk", "list"}, parse: parseAzureDisks}),
		a(ResourceType{Type: "azurerm_storage_account", idShape: azureStoreID, Service: "storage", Label: "Storage accounts", Operation: "Microsoft.Storage/storageAccounts/read", ImportID: "Full resource ID (…/storageAccounts/NAME)", StateKeys: []string{"id"}, Permissions: "Microsoft.Storage/storageAccounts/read", args: []string{"storage", "account", "list"}, parse: parseAzureStorage}),
	)
}

// azureTags reads a tag map; az reports null for none.
func azureTags(m map[string]string) []awsTag {
	tags := make([]awsTag, 0, len(m))
	for k, v := range m {
		tags = append(tags, awsTag{Key: k, Value: v})
	}
	return tags
}

// azureCandidate builds one candidate; id is the resource ID az reported. It is the import ID
// only when it has the documented shape and belongs to the verified subscription.
func azureCandidate(ctx parseContext, typ, service, key, name, location, id string, shape *regexp.Regexp, tags map[string]string, details map[string]string) CloudResourceCandidate {
	ok := shape.MatchString(id) && strings.HasPrefix(strings.ToLower(id), "/subscriptions/"+strings.ToLower(ctx.Account)+"/")
	t, _, omitted := sanitizeTags(azureTags(tags))
	c := CloudResourceCandidate{Type: typ, Service: service, ResourceID: cleanText(key, 120), Region: location, Account: ctx.Account, Name: cleanText(name, maxName), Tags: t, TagsOmitted: omitted, Details: map[string]string{}}
	for k, v := range details {
		if safeWord.MatchString(v) {
			c.Details[k] = v
		}
	}
	c = finish(c, ctx, id, ok)
	if c.ImportStatus == ImportIDVerified {
		c.CloudID = id
	}
	return c
}

func azureSelected(ctx parseContext, location string) bool {
	return len(ctx.Selected) == 0 || ctx.Selected[location]
}

// azureGroup is the resource group name of a resource ID ("" when it has none).
func azureGroup(id string) string {
	parts := strings.Split(id, "/")
	if len(parts) > 4 && strings.EqualFold(parts[3], "resourceGroups") {
		return parts[4]
	}
	return ""
}

// azureKey names a candidate by group and name: a name is unique per group, not per subscription.
func azureKey(id, name string) string {
	if g := azureGroup(id); g != "" {
		return strings.ToLower(g) + "/" + name
	}
	return name
}

type azureItem struct {
	ID, Name, Location string
	Tags               map[string]string
}

func parseAzureGroups(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []azureItem
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, g := range doc {
		if !azureLocation.MatchString(g.Location) || !azureSelected(ctx, g.Location) {
			continue
		}
		out = append(out, azureCandidate(ctx, "azurerm_resource_group", "resources", g.Name, g.Name, g.Location, g.ID, azureGroupID, g.Tags, nil))
	}
	return out, "", nil
}

type azureVNet struct {
	azureItem
	Subnets []struct{ ID, Name, Prefix string }
}

func parseAzureVNets(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []azureVNet
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, v := range doc {
		if !azureLocation.MatchString(v.Location) || !azureSelected(ctx, v.Location) {
			continue
		}
		out = append(out, azureCandidate(ctx, "azurerm_virtual_network", "network", azureKey(v.ID, v.Name), v.Name, v.Location, v.ID, azureVNetID, v.Tags, map[string]string{"resourceGroup": azureGroup(v.ID), "subnets": itoa(len(v.Subnets))}))
	}
	return out, "", nil
}

// parseAzureSubnets reads the subnets of the same list: a subnet has no location, so it takes
// its virtual network's.
func parseAzureSubnets(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []azureVNet
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, v := range doc {
		if !azureLocation.MatchString(v.Location) || !azureSelected(ctx, v.Location) {
			continue
		}
		for _, s := range v.Subnets {
			c := azureCandidate(ctx, "azurerm_subnet", "network", azureKey(s.ID, v.Name+"/"+s.Name), s.Name, v.Location, s.ID, azureSubnetID, nil, map[string]string{"resourceGroup": azureGroup(s.ID), "virtualNetwork": v.Name})
			if cidr.MatchString(s.Prefix) {
				c.Details["cidr"] = s.Prefix
			}
			out = append(out, c)
		}
	}
	return out, "", nil
}

// parseAzureNSGs reads group identity only: the security rules are not in the projection and
// are never decoded.
func parseAzureNSGs(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []azureItem
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, n := range doc {
		if !azureLocation.MatchString(n.Location) || !azureSelected(ctx, n.Location) {
			continue
		}
		out = append(out, azureCandidate(ctx, "azurerm_network_security_group", "network", azureKey(n.ID, n.Name), n.Name, n.Location, n.ID, azureNSGID, n.Tags, map[string]string{"resourceGroup": azureGroup(n.ID)}))
	}
	return out, "", nil
}

// azureVMs parses `vm list` for one OS: a Linux and a Windows machine are different types.
func azureVMs(osType, typ string) func([]byte, parseContext) ([]CloudResourceCandidate, string, error) {
	return func(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
		var doc []struct {
			azureItem
			OSType string `json:"osType"`
			Size   string `json:"size"`
		}
		if err := decodePage(page, &doc); err != nil {
			return nil, "", err
		}
		out := []CloudResourceCandidate{}
		for _, v := range doc {
			if v.OSType != osType || !azureLocation.MatchString(v.Location) || !azureSelected(ctx, v.Location) {
				continue
			}
			out = append(out, azureCandidate(ctx, typ, "compute", azureKey(v.ID, v.Name), v.Name, v.Location, v.ID, azureVMID, v.Tags, map[string]string{"resourceGroup": azureGroup(v.ID), "size": v.Size}))
		}
		return out, "", nil
	}
}

func parseAzureDisks(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		azureItem
		SizeGB int    `json:"sizeGb"`
		SKU    string `json:"sku"`
		State  string `json:"state"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, d := range doc {
		if !azureLocation.MatchString(d.Location) || !azureSelected(ctx, d.Location) {
			continue
		}
		out = append(out, azureCandidate(ctx, "azurerm_managed_disk", "compute", azureKey(d.ID, d.Name), d.Name, d.Location, d.ID, azureDiskID, d.Tags, map[string]string{"resourceGroup": azureGroup(d.ID), "sizeGb": itoa(d.SizeGB), "sku": d.SKU, "state": d.State}))
	}
	return out, "", nil
}

func parseAzureStorage(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc []struct {
		azureItem
		Kind string `json:"kind"`
		SKU  string `json:"sku"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, a := range doc {
		if !azureLocation.MatchString(a.Location) || !azureSelected(ctx, a.Location) {
			continue
		}
		out = append(out, azureCandidate(ctx, "azurerm_storage_account", "storage", azureKey(a.ID, a.Name), a.Name, a.Location, a.ID, azureStoreID, a.Tags, map[string]string{"resourceGroup": azureGroup(a.ID), "kind": a.Kind, "sku": a.SKU}))
	}
	return out, "", nil
}
