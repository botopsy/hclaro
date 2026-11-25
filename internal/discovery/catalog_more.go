package discovery

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Second batch of supported types: the common EC2 networking and compute objects, load
// balancers, IAM roles, Lambda functions and DynamoDB tables. Same rules as the first batch:
// identity and a few safe facts only, never policies, user data, environment variables,
// endpoints or other configuration.

var (
	instanceID = regexp.MustCompile(`^i-[0-9a-f]{8,17}$`)
	volumeID   = regexp.MustCompile(`^vol-[0-9a-f]{8,17}$`)
	igwID      = regexp.MustCompile(`^igw-[0-9a-f]{8,17}$`)
	rtbID      = regexp.MustCompile(`^rtb-[0-9a-f]{8,17}$`)
	natID      = regexp.MustCompile(`^nat-[0-9a-f]{8,17}$`)
	// aws_lb is imported by ARN.
	lbARN         = regexp.MustCompile(`^arn:aws(-cn|-us-gov|-iso[a-z]?)?:elasticloadbalancing:[a-z0-9-]+:[0-9]{12}:loadbalancer/(app|net|gwy)/[A-Za-z0-9-]{1,32}/[0-9a-f]{16}$`)
	iamRoleName   = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)
	lambdaName    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	dynamoTable   = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,255}$`)
	sqsURL        = regexp.MustCompile(`^https://sqs\.[a-z0-9-]+\.amazonaws\.com(\.cn)?/([0-9]{12})/([A-Za-z0-9_-]{1,80}(\.fifo)?)$`)
	snsARN        = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:sns:[a-z0-9-]+:([0-9]{12}):([A-Za-z0-9_-]{1,256}(\.fifo)?)$`)
	kmsKeyID      = regexp.MustCompile(`^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|mrk-[0-9a-f]{32})$`)
	logGroupName  = regexp.MustCompile(`^[A-Za-z0-9_./#-]{1,512}$`)
	iamPath       = regexp.MustCompile(`^/([A-Za-z0-9+=,.@_-]+/)*$`)
	lbTypeAllowed = map[string]bool{"application": true, "network": true, "gateway": true}
)

// item is one parsed object before the shared fields are filled in.
type item struct {
	typ, service, id, arn, owner, name string
	tags                               []awsTag
	details                            map[string]string
	valid                              bool
}

// candidate builds the shared candidate fields and decides the import ID status.
func (it item) candidate(ctx parseContext) CloudResourceCandidate {
	tags, tagName, omitted := sanitizeTags(it.tags)
	name := tagName
	if name == "" {
		name = cleanText(it.name, maxName)
	}
	account := ownerOrSelf(it.owner, ctx)
	c := CloudResourceCandidate{Type: it.typ, Service: it.service, ResourceID: cleanText(it.id, 255), ARN: it.arn, Region: ctx.Region, Account: account, Name: name, Tags: tags, TagsOmitted: omitted, Details: map[string]string{}}
	for k, v := range it.details {
		if safeWord.MatchString(v) {
			c.Details[k] = v
		}
	}
	return finish(c, ctx, it.id, it.valid)
}

func (ctx parseContext) arn(service, resource string) string {
	return "arn:" + ctx.Partition + ":" + service + ":" + ctx.Region + ":" + ctx.Account + ":" + resource
}

// parseInstances skips terminated instances: they stay listed for about an hour but no
// longer exist. User data, addresses and network interfaces are never decoded.
func parseInstances(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Reservations []struct {
			OwnerID   string `json:"OwnerId"`
			Instances []struct {
				InstanceID   string                `json:"InstanceId"`
				InstanceType string                `json:"InstanceType"`
				VpcID        string                `json:"VpcId"`
				SubnetID     string                `json:"SubnetId"`
				State        struct{ Name string } `json:"State"`
				Tags         []awsTag              `json:"Tags"`
			} `json:"Instances"`
		} `json:"Reservations"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, r := range doc.Reservations {
		for _, i := range r.Instances {
			if i.State.Name == "terminated" {
				continue
			}
			it := item{typ: "aws_instance", service: "ec2", id: i.InstanceID, owner: r.OwnerID, tags: i.Tags, valid: instanceID.MatchString(i.InstanceID),
				details: map[string]string{"instanceType": i.InstanceType, "state": i.State.Name}}
			if vpcID.MatchString(i.VpcID) {
				it.details["vpc"] = i.VpcID
			}
			if subnetID.MatchString(i.SubnetID) {
				it.details["subnet"] = i.SubnetID
			}
			if it.valid {
				it.arn = ctx.arn("ec2", "instance/"+i.InstanceID)
			}
			out = append(out, it.candidate(ctx))
		}
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

func parseVolumes(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Volumes []struct {
			VolumeID         string   `json:"VolumeId"`
			Size             int      `json:"Size"`
			VolumeType       string   `json:"VolumeType"`
			State            string   `json:"State"`
			AvailabilityZone string   `json:"AvailabilityZone"`
			Tags             []awsTag `json:"Tags"`
		} `json:"Volumes"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, v := range doc.Volumes {
		it := item{typ: "aws_ebs_volume", service: "ec2", id: v.VolumeID, tags: v.Tags, valid: volumeID.MatchString(v.VolumeID),
			details: map[string]string{"type": v.VolumeType, "state": v.State}}
		if v.Size > 0 {
			it.details["sizeGiB"] = itoa(v.Size)
		}
		if azName.MatchString(v.AvailabilityZone) {
			it.details["availabilityZone"] = v.AvailabilityZone
		}
		if it.valid {
			it.arn = ctx.arn("ec2", "volume/"+v.VolumeID)
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

func parseInternetGateways(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Gateways []struct {
			ID          string   `json:"InternetGatewayId"`
			OwnerID     string   `json:"OwnerId"`
			Tags        []awsTag `json:"Tags"`
			Attachments []struct {
				VpcID string `json:"VpcId"`
			} `json:"Attachments"`
		} `json:"InternetGateways"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, g := range doc.Gateways {
		it := item{typ: "aws_internet_gateway", service: "ec2", id: g.ID, owner: g.OwnerID, tags: g.Tags, valid: igwID.MatchString(g.ID), details: map[string]string{}}
		if len(g.Attachments) > 0 && vpcID.MatchString(g.Attachments[0].VpcID) {
			it.details["vpc"] = g.Attachments[0].VpcID
		}
		if it.valid {
			it.arn = ctx.arn("ec2", "internet-gateway/"+g.ID)
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseRouteTables reads identity and the VPC only; routes and associations are not decoded.
func parseRouteTables(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Tables []struct {
			ID      string   `json:"RouteTableId"`
			VpcID   string   `json:"VpcId"`
			OwnerID string   `json:"OwnerId"`
			Tags    []awsTag `json:"Tags"`
		} `json:"RouteTables"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, t := range doc.Tables {
		it := item{typ: "aws_route_table", service: "ec2", id: t.ID, owner: t.OwnerID, tags: t.Tags, valid: rtbID.MatchString(t.ID), details: map[string]string{}}
		if vpcID.MatchString(t.VpcID) {
			it.details["vpc"] = t.VpcID
		}
		if it.valid {
			it.arn = ctx.arn("ec2", "route-table/"+t.ID)
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseNATGateways skips deleted and deleting gateways, which stay listed for about an hour.
func parseNATGateways(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Gateways []struct {
			ID           string   `json:"NatGatewayId"`
			VpcID        string   `json:"VpcId"`
			SubnetID     string   `json:"SubnetId"`
			State        string   `json:"State"`
			Connectivity string   `json:"ConnectivityType"`
			Tags         []awsTag `json:"Tags"`
		} `json:"NatGateways"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, g := range doc.Gateways {
		if g.State == "deleted" || g.State == "deleting" {
			continue
		}
		it := item{typ: "aws_nat_gateway", service: "ec2", id: g.ID, tags: g.Tags, valid: natID.MatchString(g.ID),
			details: map[string]string{"state": g.State, "connectivity": g.Connectivity}}
		if vpcID.MatchString(g.VpcID) {
			it.details["vpc"] = g.VpcID
		}
		if subnetID.MatchString(g.SubnetID) {
			it.details["subnet"] = g.SubnetID
		}
		if it.valid {
			it.arn = ctx.arn("ec2", "natgateway/"+g.ID)
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseLoadBalancers covers Application, Network and Gateway load balancers (aws_lb). DNS
// names, listeners and security groups are not decoded. The import ID is the ARN.
func parseLoadBalancers(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		LoadBalancers []struct {
			ARN   string `json:"LoadBalancerArn"`
			Name  string `json:"LoadBalancerName"`
			Type  string `json:"Type"`
			VpcID string `json:"VpcId"`
			State struct {
				Code string `json:"Code"`
			} `json:"State"`
		} `json:"LoadBalancers"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, l := range doc.LoadBalancers {
		it := item{typ: "aws_lb", service: "elasticloadbalancing", id: l.ARN, arn: l.ARN, name: l.Name, valid: lbARN.MatchString(l.ARN) && lbTypeAllowed[l.Type], details: map[string]string{"state": l.State.Code}}
		if lbTypeAllowed[l.Type] {
			it.details["type"] = l.Type
		}
		if vpcID.MatchString(l.VpcID) {
			it.details["vpc"] = l.VpcID
		}
		if parts := strings.Split(l.ARN, ":"); len(parts) > 4 && accountRE.MatchString(parts[4]) {
			it.owner = parts[4]
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseRoles reads the role name, path and ARN only. The trust policy
// (AssumeRolePolicyDocument) describes who may assume the role and is never decoded.
func parseRoles(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Roles []struct {
			Name string `json:"RoleName"`
			ARN  string `json:"Arn"`
			Path string `json:"Path"`
		} `json:"Roles"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, r := range doc.Roles {
		it := item{typ: "aws_iam_role", service: "iam", id: r.Name, arn: r.ARN, name: r.Name, valid: iamRoleName.MatchString(r.Name), details: map[string]string{}}
		if parts := strings.Split(r.ARN, ":"); len(parts) > 4 && accountRE.MatchString(parts[4]) {
			it.owner = parts[4]
		}
		if strings.HasPrefix(r.Path, "/aws-service-role/") && iamPath.MatchString(r.Path) {
			it.details["serviceLinked"] = "true"
		}
		c := it.candidate(ctx)
		// IAM is global: the object is not tied to a scanned region.
		c.Regions = []string{}
		out = append(out, c)
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseFunctions reads the name, runtime and state only. Environment variables, code
// locations and layers are never decoded.
func parseFunctions(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Functions []struct {
			Name    string `json:"FunctionName"`
			ARN     string `json:"FunctionArn"`
			Runtime string `json:"Runtime"`
		} `json:"Functions"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, f := range doc.Functions {
		it := item{typ: "aws_lambda_function", service: "lambda", id: f.Name, arn: f.ARN, name: f.Name, valid: lambdaName.MatchString(f.Name), details: map[string]string{"runtime": f.Runtime}}
		if parts := strings.Split(f.ARN, ":"); len(parts) > 4 && accountRE.MatchString(parts[4]) {
			it.owner = parts[4]
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseTables reads table names only: `dynamodb list-tables` returns nothing else.
func parseTables(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		TableNames []string `json:"TableNames"`
		NextToken  string   `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, n := range doc.TableNames {
		it := item{typ: "aws_dynamodb_table", service: "dynamodb", id: n, name: n, valid: dynamoTable.MatchString(n), details: map[string]string{}}
		if it.valid {
			it.arn = ctx.arn("dynamodb", "table/"+n)
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseQueues reads queue URLs only (the import ID of aws_sqs_queue); attributes and policies
// need per-queue calls and are never read.
func parseQueues(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		QueueURLs []string `json:"QueueUrls"`
		NextToken string   `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, u := range doc.QueueURLs {
		m := sqsURL.FindStringSubmatch(u)
		it := item{typ: "aws_sqs_queue", service: "sqs", id: u, valid: m != nil, details: map[string]string{}}
		if m != nil {
			it.owner, it.name = m[2], m[3]
			it.arn = "arn:" + ctx.Partition + ":sqs:" + ctx.Region + ":" + m[2] + ":" + m[3]
			if m[4] != "" {
				it.details["fifo"] = "true"
			}
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseTopics reads topic ARNs only (the import ID of aws_sns_topic); policies and
// subscriptions are never read.
func parseTopics(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Topics []struct {
			ARN string `json:"TopicArn"`
		} `json:"Topics"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, t := range doc.Topics {
		m := snsARN.FindStringSubmatch(t.ARN)
		it := item{typ: "aws_sns_topic", service: "sns", id: t.ARN, arn: t.ARN, valid: m != nil, details: map[string]string{}}
		if m != nil {
			it.owner, it.name = m[2], m[3]
			if m[4] != "" {
				it.details["fifo"] = "true"
			}
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseKeyAliases lists customer-managed KMS keys through their aliases. Keys without an alias
// are not listed, and AWS-managed keys (alias/aws/...) are skipped: Terraform cannot manage
// them, so they would only be noise. Key policies and grants are never read.
func parseKeyAliases(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Aliases []struct {
			Name        string `json:"AliasName"`
			TargetKeyID string `json:"TargetKeyId"`
		} `json:"Aliases"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	seen := map[string]bool{}
	for _, a := range doc.Aliases {
		if a.TargetKeyID == "" || strings.HasPrefix(a.Name, "alias/aws/") || seen[a.TargetKeyID] {
			continue
		}
		seen[a.TargetKeyID] = true
		it := item{typ: "aws_kms_key", service: "kms", id: a.TargetKeyID, name: strings.TrimPrefix(a.Name, "alias/"), valid: kmsKeyID.MatchString(a.TargetKeyID), details: map[string]string{}}
		if it.valid {
			it.arn = ctx.arn("kms", "key/"+a.TargetKeyID)
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseLogGroups reads the name, retention and class. Stored bytes, KMS key and metric
// filters are not decoded.
func parseLogGroups(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Groups []struct {
			Name      string `json:"logGroupName"`
			ARN       string `json:"arn"`
			Retention int    `json:"retentionInDays"`
			Class     string `json:"logGroupClass"`
		} `json:"logGroups"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, g := range doc.Groups {
		// The CLI reports the ARN with a trailing ":*".
		arn := strings.TrimSuffix(g.ARN, ":*")
		it := item{typ: "aws_cloudwatch_log_group", service: "logs", id: g.Name, arn: arn, name: g.Name, valid: logGroupName.MatchString(g.Name), details: map[string]string{"class": g.Class}}
		if g.Retention > 0 {
			it.details["retentionDays"] = itoa(g.Retention)
		}
		if parts := strings.Split(arn, ":"); len(parts) > 4 && accountRE.MatchString(parts[4]) {
			it.owner = parts[4]
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

var (
	// ECR repository names: lower case path segments separated by "/", 2-256 characters.
	ecrRepoName    = regexp.MustCompile(`^([a-z0-9]+([._-][a-z0-9]+)*/)*[a-z0-9]+([._-][a-z0-9]+)*$`)
	eksClusterName = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9_-]{0,99}$`)
	// ecs list-clusters returns ARNs; aws_ecs_cluster is imported by the cluster name.
	ecsClusterARN  = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:ecs:[a-z0-9-]+:([0-9]{12}):cluster/([A-Za-z0-9_-]{1,255})$`)
	ecsClusterName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
	hostedZoneID   = regexp.MustCompile(`^Z[A-Z0-9]{1,31}$`)
)

// parseRepositories reads the repository name, ARN and tag mutability. Encryption settings,
// URIs and scanning configuration are not decoded.
func parseRepositories(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Repositories []struct {
			Name       string `json:"repositoryName"`
			ARN        string `json:"repositoryArn"`
			Mutability string `json:"imageTagMutability"`
		} `json:"repositories"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, r := range doc.Repositories {
		it := item{typ: "aws_ecr_repository", service: "ecr", id: r.Name, arn: r.ARN, name: r.Name, valid: len(r.Name) >= 2 && len(r.Name) <= 256 && ecrRepoName.MatchString(r.Name), details: map[string]string{"tagMutability": r.Mutability}}
		if parts := strings.Split(r.ARN, ":"); len(parts) > 4 && accountRE.MatchString(parts[4]) {
			it.owner = parts[4]
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseEKSClusters reads cluster names only (`eks list-clusters` returns nothing else).
func parseEKSClusters(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Clusters  []string `json:"clusters"`
		NextToken string   `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, n := range doc.Clusters {
		it := item{typ: "aws_eks_cluster", service: "eks", id: n, name: n, valid: eksClusterName.MatchString(n), details: map[string]string{}}
		if it.valid {
			it.arn = ctx.arn("eks", "cluster/"+n)
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseECSClusters reads cluster ARNs and derives the name, which is the import ID.
func parseECSClusters(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		ClusterARNs []string `json:"clusterArns"`
		NextToken   string   `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, a := range doc.ClusterARNs {
		m := ecsClusterARN.FindStringSubmatch(a)
		it := item{typ: "aws_ecs_cluster", service: "ecs", id: a, arn: a, valid: m != nil, details: map[string]string{}}
		if m != nil {
			it.id, it.owner, it.name = m[3], m[2], m[3]
		}
		c := it.candidate(ctx)
		if m == nil {
			c.ResourceID = cleanText(a, 255)
		}
		out = append(out, c)
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseHostedZones reads the zone ID, name and whether it is private. Comments, caller
// references and delegation sets are not decoded. Route 53 is global: zones are not tied to a
// scanned region.
func parseHostedZones(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Zones []struct {
			ID     string `json:"Id"`
			Name   string `json:"Name"`
			Count  int    `json:"ResourceRecordSetCount"`
			Config struct {
				Private bool `json:"PrivateZone"`
			} `json:"Config"`
		} `json:"HostedZones"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, z := range doc.Zones {
		id := strings.TrimPrefix(z.ID, "/hostedzone/")
		it := item{typ: "aws_route53_zone", service: "route53", id: id, name: strings.TrimSuffix(z.Name, "."), valid: hostedZoneID.MatchString(id), details: map[string]string{}}
		if it.valid {
			it.arn = "arn:" + ctx.Partition + ":route53:::hostedzone/" + id
		}
		if z.Config.Private {
			it.details["private"] = "true"
		}
		if z.Count > 0 {
			it.details["records"] = itoa(z.Count)
		}
		c := it.candidate(ctx)
		c.Regions = []string{}
		out = append(out, c)
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

var (
	acmARN       = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:acm:[a-z0-9-]+:([0-9]{12}):certificate/[0-9a-f-]{36}$`)
	secretARN    = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:secretsmanager:[a-z0-9-]+:([0-9]{12}):secret:[A-Za-z0-9/_+=.@-]{1,512}-[A-Za-z0-9]{6}$`)
	ssmParamName = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`) // regexp caps repeats at 1000: the 2048-character limit is checked in parseParameters
	stateMachARN = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:states:[a-z0-9-]+:([0-9]{12}):stateMachine:[A-Za-z0-9_-]{1,80}$`)
)

// ownerFromARN is the account an ARN names, or "" when it has none.
func ownerFromARN(arn string) string {
	if parts := strings.Split(arn, ":"); len(parts) > 4 && accountRE.MatchString(parts[4]) {
		return parts[4]
	}
	return ""
}

// parseCertificates reads the certificate ARN, domain, status and type. Subject alternative
// names, key algorithms and in-use associations are not decoded.
func parseCertificates(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Certificates []struct {
			ARN    string `json:"CertificateArn"`
			Domain string `json:"DomainName"`
			Status string `json:"Status"`
			Type   string `json:"Type"`
		} `json:"CertificateSummaryList"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, c := range doc.Certificates {
		it := item{typ: "aws_acm_certificate", service: "acm", id: c.ARN, arn: c.ARN, name: c.Domain, owner: ownerFromARN(c.ARN), valid: acmARN.MatchString(c.ARN), details: map[string]string{"status": c.Status, "type": c.Type}}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseSecrets reads the secret ARN, name and tags. Secret values are never part of a list
// response and descriptions are free text, so neither is decoded. Secrets already scheduled
// for deletion are skipped.
func parseSecrets(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Secrets []struct {
			ARN         string          `json:"ARN"`
			Name        string          `json:"Name"`
			DeletedDate json.RawMessage `json:"DeletedDate"`
			Tags        []awsTag        `json:"Tags"`
		} `json:"SecretList"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, sec := range doc.Secrets {
		if d := string(sec.DeletedDate); d != "" && d != "null" {
			continue
		}
		it := item{typ: "aws_secretsmanager_secret", service: "secretsmanager", id: sec.ARN, arn: sec.ARN, name: sec.Name, owner: ownerFromARN(sec.ARN), tags: sec.Tags, valid: secretARN.MatchString(sec.ARN), details: map[string]string{}}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseParameters reads the parameter name, type and tier. `ssm describe-parameters` returns
// metadata only; values and descriptions are never decoded.
func parseParameters(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		Parameters []struct {
			Name string `json:"Name"`
			Type string `json:"Type"`
			Tier string `json:"Tier"`
		} `json:"Parameters"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, p := range doc.Parameters {
		it := item{typ: "aws_ssm_parameter", service: "ssm", id: p.Name, name: p.Name, valid: len(p.Name) <= 2048 && ssmParamName.MatchString(p.Name), details: map[string]string{"type": p.Type, "tier": p.Tier}}
		if it.valid {
			it.arn = ctx.arn("ssm", "parameter/"+strings.TrimPrefix(p.Name, "/"))
		}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}

// parseStateMachines reads the ARN, name and type. Definitions and role ARNs are not part of
// the list response and are never read.
func parseStateMachines(page []byte, ctx parseContext) ([]CloudResourceCandidate, string, error) {
	var doc struct {
		StateMachines []struct {
			ARN  string `json:"stateMachineArn"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"stateMachines"`
		NextToken string `json:"NextToken"`
	}
	if err := decodePage(page, &doc); err != nil {
		return nil, "", err
	}
	out := []CloudResourceCandidate{}
	for _, m := range doc.StateMachines {
		it := item{typ: "aws_sfn_state_machine", service: "states", id: m.ARN, arn: m.ARN, name: m.Name, owner: ownerFromARN(m.ARN), valid: stateMachARN.MatchString(m.ARN), details: map[string]string{"type": m.Type}}
		out = append(out, it.candidate(ctx))
	}
	token, err := nextToken(doc.NextToken)
	return out, token, err
}
