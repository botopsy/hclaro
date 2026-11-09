package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"hclaro/internal/state"
)

// Import ID mapping statuses of a candidate.
const (
	// ImportIDVerified: the import ID follows the documented import syntax of the type and
	// passed Hclaro's credential-shape checks.
	ImportIDVerified = "verified"
	// ImportIDUnresolved: no safe import ID could be derived (missing, credential-shaped, or
	// the object is owned by another account and only shared with this one).
	ImportIDUnresolved = "unresolved"
)

// Tag is one sanitized tag. Values that look like credentials are replaced.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CloudResourceCandidate is one existing AWS object as discovered, normalized and
// sanitized. It holds minimal display metadata only: no policies, rules, endpoints,
// credentials or full configuration.
type CloudResourceCandidate struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Service    string `json:"service"`
	ResourceID string `json:"resourceId"`
	ARN        string `json:"arn,omitempty"`
	Region     string `json:"region"`
	Account    string `json:"account"`
	Name       string `json:"name,omitempty"`
	Tags       []Tag  `json:"tags"`
	// TagsOmitted counts tags beyond the display limit or with unsafe keys.
	TagsOmitted int `json:"tagsOmitted,omitempty"`
	// Details are a few safe facts per type (CIDR, VPC, engine, …).
	Details      map[string]string `json:"details"`
	ImportID     string            `json:"importId,omitempty"`
	ImportStatus string            `json:"importIdStatus"`
	ImportNote   string            `json:"importIdNote,omitempty"`
	ObservedAt   string            `json:"observedAt"`
	// Regions lists every scanned region that reported this object (multi-region duplicates
	// are merged into one candidate).
	Regions []string `json:"seenInRegions"`
}

// ResourceType documents one supported type: the AWS CLI call that lists it, whether it is
// global (listed once, not per region), the documented import ID and the state attributes
// that hold the same identifier.
type ResourceType struct {
	Type        string   `json:"type"`
	Service     string   `json:"service"`
	Label       string   `json:"label"`
	Global      bool     `json:"global"`
	Operation   string   `json:"operation"`
	ImportID    string   `json:"importIdFormat"`
	StateKeys   []string `json:"-"`
	Permissions string   `json:"permission"`
	// idShape is the documented import ID format; a state identifier in another format is
	// not compared (it is not proof that the object is untracked).
	idShape *regexp.Regexp
	args    []string
	parse   func(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error)
}

// parseContext carries what a page parser needs to build ARNs and fill the account.
type parseContext struct {
	Region    string
	Account   string
	Partition string
	Observed  string
	Selected  map[string]bool
}

// Types is the allowlist of supported resource types, in display order. Only types with
// tested list semantics and a documented import ID (hashicorp/aws v5) are included.
var Types []ResourceType

func init() {
	Types = []ResourceType{
		{Type: "aws_vpc", idShape: vpcID, Service: "ec2", Label: "VPCs", Operation: "ec2:DescribeVpcs", ImportID: "VPC ID (vpc-…)", StateKeys: []string{"id"}, Permissions: "ec2:DescribeVpcs", args: []string{"ec2", "describe-vpcs"}, parse: parseVPCs},
		{Type: "aws_subnet", idShape: subnetID, Service: "ec2", Label: "Subnets", Operation: "ec2:DescribeSubnets", ImportID: "Subnet ID (subnet-…)", StateKeys: []string{"id"}, Permissions: "ec2:DescribeSubnets", args: []string{"ec2", "describe-subnets"}, parse: parseSubnets},
		{Type: "aws_security_group", idShape: sgID, Service: "ec2", Label: "Security groups", Operation: "ec2:DescribeSecurityGroups", ImportID: "Security group ID (sg-…)", StateKeys: []string{"id"}, Permissions: "ec2:DescribeSecurityGroups", args: []string{"ec2", "describe-security-groups"}, parse: parseSecurityGroups},
		{Type: "aws_s3_bucket", idShape: bucketName, Service: "s3", Label: "S3 buckets", Global: true, Operation: "s3:ListAllMyBuckets", ImportID: "Bucket name", StateKeys: []string{"id", "bucket"}, Permissions: "s3:ListAllMyBuckets", args: []string{"s3api", "list-buckets"}, parse: parseBuckets},
		{Type: "aws_db_instance", idShape: dbID, Service: "rds", Label: "RDS DB instances", Operation: "rds:DescribeDBInstances", ImportID: "DB instance identifier", StateKeys: []string{"identifier"}, Permissions: "rds:DescribeDBInstances", args: []string{"rds", "describe-db-instances"}, parse: parseDBInstances},
	}
}

// TypeByName returns a supported type.
func TypeByName(name string) (ResourceType, bool) {
	for _, t := range Types {
		if t.Type == name {
			return t, true
		}
	}
	return ResourceType{}, false
}

// Regions are the commercial AWS regions a scan can select. Opt-in regions are listed;
// a disabled one reports region_unavailable for its tasks.
var Regions = []string{
	"us-east-1", "us-east-2", "us-west-1", "us-west-2", "ca-central-1", "ca-west-1", "sa-east-1", "mx-central-1",
	"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-central-2", "eu-north-1", "eu-south-1", "eu-south-2",
	"af-south-1", "me-south-1", "me-central-1", "il-central-1",
	"ap-east-1", "ap-south-1", "ap-south-2", "ap-southeast-1", "ap-southeast-2", "ap-southeast-3", "ap-southeast-4", "ap-southeast-5", "ap-southeast-7",
	"ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
}

// KnownRegion reports whether r is a selectable region.
func KnownRegion(r string) bool {
	for _, k := range Regions {
		if k == r {
			return true
		}
	}
	return false
}

// awsTag is the EC2/RDS tag shape.
type awsTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

var (
	vpcID     = regexp.MustCompile(`^vpc-[0-9a-f]{8,17}$`)
	subnetID  = regexp.MustCompile(`^subnet-[0-9a-f]{8,17}$`)
	sgID      = regexp.MustCompile(`^sg-[0-9a-f]{8,17}$`)
	accountRE = regexp.MustCompile(`^[0-9]{12}$`)
	// Bucket names: 3-63 characters, lower case, digits, dots and hyphens.
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	dbID       = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,62}$`)
	cidr       = regexp.MustCompile(`^[0-9a-f:.]{2,45}/[0-9]{1,3}$`)
	azName     = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-[0-9]{1,2}[a-z]$`)
	safeWord   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	arnShape   = regexp.MustCompile(`^arn:aws(-cn|-us-gov|-iso[a-z]?)?:[a-z0-9-]{1,20}:[a-z0-9-]*:[0-9]{0,12}:[A-Za-z0-9+=,.@/:_-]{1,512}$`)
)

// candidateID is a stable opaque ID from the type and the provider identifier.
func candidateID(typ, account, id string) string {
	sum := sha256.Sum256([]byte("hclaro-discovery-v1\x00" + typ + "\x00" + account + "\x00" + id))
	return hex.EncodeToString(sum[:16])
}

const (
	maxTags     = 12
	maxTagKey   = 128
	maxTagValue = 256
	maxName     = 128
)

// cleanText makes untrusted text safe to display: control and bidirectional formatting
// characters are removed, length is bounded, and credential-shaped values are withheld.
func cleanText(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len([]rune(out)) > limit {
		out = string([]rune(out)[:limit]) + "…"
	}
	if state.LooksSecret(out) {
		return state.Redacted
	}
	return out
}

// sanitizeTags keeps at most maxTags tags with safe keys, sorted by key. Values of risky keys
// (password, token, …) and credential-shaped values are redacted.
func sanitizeTags(tags []awsTag) (out []Tag, name string, omitted int) {
	out = []Tag{}
	sorted := append([]awsTag(nil), tags...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	for _, t := range sorted {
		key := cleanText(t.Key, maxTagKey)
		if key == "" || key == state.Redacted || state.Suspicious(t.Key) {
			omitted++
			continue
		}
		value := cleanText(t.Value, maxTagValue)
		if state.RiskyKey(key) {
			value = state.Redacted
		}
		if key == "Name" && value != state.Redacted {
			name = cleanText(t.Value, maxName)
		}
		if len(out) >= maxTags {
			omitted++
			continue
		}
		out = append(out, Tag{key, value})
	}
	return out, name, omitted
}

// finish fills the fields shared by every candidate and decides the import ID status.
func finish(c CloudResourceCandidate, ctx parseContext, importID string, valid bool) CloudResourceCandidate {
	c.ObservedAt = ctx.Observed
	c.Regions = []string{c.Region}
	if c.Details == nil {
		c.Details = map[string]string{}
	}
	if c.Tags == nil {
		c.Tags = []Tag{}
	}
	if c.ARN != "" && (!arnShape.MatchString(c.ARN) || state.Suspicious(c.ARN)) {
		c.ARN = ""
	}
	switch {
	case !valid || importID == "" || state.Suspicious(importID):
		c.ImportStatus, c.ImportNote = ImportIDUnresolved, "No import ID in the documented format could be derived for this object."
	case c.Account != "" && ctx.Account != "" && c.Account != ctx.Account:
		c.ImportStatus, c.ImportNote = ImportIDUnresolved, "Owned by account "+c.Account+" and shared with the verified account. Import it from the owner account."
	default:
		c.ImportStatus, c.ImportID = ImportIDVerified, importID
	}
	c.ID = candidateID(c.Type, c.Account, c.ResourceID)
	return c
}

func decodePage(page []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(page)))
	if err := dec.Decode(v); err != nil {
		return &CallError{KindMalformed}
	}
	return nil
}

// nextToken validates the pagination token an AWS CLI page returned.
func nextToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	if !tokenPattern.MatchString(token) {
		return "", &CallError{KindMalformed}
	}
	return token, nil
}

func ownerOrSelf(owner string, ctx parseContext) string {
	if accountRE.MatchString(owner) {
		return owner
	}
	return ctx.Account
}

func parseVPCs(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Vpcs []struct {
			VpcID     string   `json:"VpcId"`
			OwnerID   string   `json:"OwnerId"`
			CidrBlock string   `json:"CidrBlock"`
			IsDefault bool     `json:"IsDefault"`
			State     string   `json:"State"`
			Tags      []awsTag `json:"Tags"`
		} `json:"Vpcs"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, v := range doc.Vpcs {
		tags, name, omitted := sanitizeTags(v.Tags)
		account := ownerOrSelf(v.OwnerID, ctx)
		c := CloudResourceCandidate{Type: "aws_vpc", Service: "ec2", ResourceID: cleanText(v.VpcID, 64), Region: ctx.Region, Account: account, Name: name, Tags: tags, TagsOmitted: omitted, Details: map[string]string{}}
		if vpcID.MatchString(v.VpcID) {
			c.ARN = "arn:" + ctx.Partition + ":ec2:" + ctx.Region + ":" + account + ":vpc/" + v.VpcID
		}
		if cidr.MatchString(v.CidrBlock) {
			c.Details["cidr"] = v.CidrBlock
		}
		if v.IsDefault {
			c.Details["default"] = "true"
		}
		if safeWord.MatchString(v.State) {
			c.Details["state"] = v.State
		}
		out = append(out, finish(c, ctx, v.VpcID, vpcID.MatchString(v.VpcID)))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

func parseSubnets(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Subnets []struct {
			SubnetID         string   `json:"SubnetId"`
			SubnetArn        string   `json:"SubnetArn"`
			VpcID            string   `json:"VpcId"`
			OwnerID          string   `json:"OwnerId"`
			CidrBlock        string   `json:"CidrBlock"`
			AvailabilityZone string   `json:"AvailabilityZone"`
			Tags             []awsTag `json:"Tags"`
		} `json:"Subnets"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, s := range doc.Subnets {
		tags, name, omitted := sanitizeTags(s.Tags)
		account := ownerOrSelf(s.OwnerID, ctx)
		c := CloudResourceCandidate{Type: "aws_subnet", Service: "ec2", ResourceID: cleanText(s.SubnetID, 64), ARN: s.SubnetArn, Region: ctx.Region, Account: account, Name: name, Tags: tags, TagsOmitted: omitted, Details: map[string]string{}}
		if c.ARN == "" && subnetID.MatchString(s.SubnetID) {
			c.ARN = "arn:" + ctx.Partition + ":ec2:" + ctx.Region + ":" + account + ":subnet/" + s.SubnetID
		}
		if vpcID.MatchString(s.VpcID) {
			c.Details["vpc"] = s.VpcID
		}
		if cidr.MatchString(s.CidrBlock) {
			c.Details["cidr"] = s.CidrBlock
		}
		if azName.MatchString(s.AvailabilityZone) {
			c.Details["availabilityZone"] = s.AvailabilityZone
		}
		out = append(out, finish(c, ctx, s.SubnetID, subnetID.MatchString(s.SubnetID)))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseSecurityGroups reads group identity only. Rules (IpPermissions) and descriptions are
// never decoded: they describe access and are free text.
func parseSecurityGroups(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Groups []struct {
			GroupID   string   `json:"GroupId"`
			GroupName string   `json:"GroupName"`
			GroupArn  string   `json:"SecurityGroupArn"`
			VpcID     string   `json:"VpcId"`
			OwnerID   string   `json:"OwnerId"`
			Tags      []awsTag `json:"Tags"`
		} `json:"SecurityGroups"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, g := range doc.Groups {
		tags, name, omitted := sanitizeTags(g.Tags)
		if name == "" {
			name = cleanText(g.GroupName, maxName)
		}
		account := ownerOrSelf(g.OwnerID, ctx)
		c := CloudResourceCandidate{Type: "aws_security_group", Service: "ec2", ResourceID: cleanText(g.GroupID, 64), ARN: g.GroupArn, Region: ctx.Region, Account: account, Name: name, Tags: tags, TagsOmitted: omitted, Details: map[string]string{}}
		if c.ARN == "" && sgID.MatchString(g.GroupID) {
			c.ARN = "arn:" + ctx.Partition + ":ec2:" + ctx.Region + ":" + account + ":security-group/" + g.GroupID
		}
		if vpcID.MatchString(g.VpcID) {
			c.Details["vpc"] = g.VpcID
		}
		if g.GroupName == "default" {
			c.Details["default"] = "true"
		}
		out = append(out, finish(c, ctx, g.GroupID, sgID.MatchString(g.GroupID)))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseBuckets reads bucket names and, when the CLI reports it, the bucket region. The owner
// block is not decoded. Buckets in regions outside the scan are skipped.
func parseBuckets(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Buckets []struct {
			Name         string `json:"Name"`
			BucketRegion string `json:"BucketRegion"`
			CreationDate string `json:"CreationDate"`
		} `json:"Buckets"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, b := range doc.Buckets {
		region := ""
		if regionPattern.MatchString(b.BucketRegion) {
			region = b.BucketRegion
			if len(ctx.Selected) > 0 && !ctx.Selected[region] {
				continue
			}
		}
		valid := bucketName.MatchString(b.Name) && !strings.Contains(b.Name, "..")
		c := CloudResourceCandidate{Type: "aws_s3_bucket", Service: "s3", ResourceID: cleanText(b.Name, 64), Region: region, Account: ctx.Account, Name: cleanText(b.Name, 64), Details: map[string]string{}}
		if valid {
			c.ARN = "arn:" + ctx.Partition + ":s3:::" + b.Name
		}
		if region == "" {
			c.Details["region"] = "not reported by this AWS CLI version"
		}
		cand := finish(c, ctx, b.Name, valid)
		if region == "" {
			cand.Regions = []string{}
		}
		out = append(out, cand)
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseDBInstances reads identity, engine and status only. Endpoints, master user names,
// parameter groups and the rest of the configuration are never decoded.
func parseDBInstances(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Instances []struct {
			Identifier string   `json:"DBInstanceIdentifier"`
			Arn        string   `json:"DBInstanceArn"`
			Engine     string   `json:"Engine"`
			Class      string   `json:"DBInstanceClass"`
			Status     string   `json:"DBInstanceStatus"`
			Tags       []awsTag `json:"TagList"`
		} `json:"DBInstances"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, d := range doc.Instances {
		tags, name, omitted := sanitizeTags(d.Tags)
		account := ctx.Account
		if parts := strings.Split(d.Arn, ":"); len(parts) > 4 && accountRE.MatchString(parts[4]) {
			account = parts[4]
		}
		if name == "" {
			name = cleanText(d.Identifier, maxName)
		}
		c := CloudResourceCandidate{Type: "aws_db_instance", Service: "rds", ResourceID: cleanText(d.Identifier, 64), ARN: d.Arn, Region: ctx.Region, Account: account, Name: name, Tags: tags, TagsOmitted: omitted, Details: map[string]string{}}
		for key, value := range map[string]string{"engine": d.Engine, "class": d.Class, "status": d.Status} {
			if safeWord.MatchString(value) {
				c.Details[key] = value
			}
		}
		out = append(out, finish(c, ctx, d.Identifier, dbID.MatchString(d.Identifier) && !strings.Contains(d.Identifier, "--")))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}
