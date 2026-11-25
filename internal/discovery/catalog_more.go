package discovery

import (
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
