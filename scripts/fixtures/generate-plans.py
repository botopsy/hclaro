# Run: python3 -I scripts/fixtures/generate-plans.py fixtures/plan
# Generates synthetic Terraform plan JSON fixtures (format 1.2) for Hclaro tests.
import json, sys, os
out = sys.argv[1]
C = "DO_NOT_LEAK_HCLARO_TEST_SECRET"
AWS = "registry.terraform.io/hashicorp/aws"
RND = "registry.terraform.io/hashicorp/random"

def rc(address, actions, before=None, after=None, unknown=None, bs=None, as_=None, mode="managed", module=None, index=None, reason=None, replace_paths=None, provider=AWS, deposed=None, previous=None, importing=None):
    rtype, name = address.split(".")[-2], address.split(".")[-1].split("[")[0]
    if mode == "data":
        rtype = address.split("data.")[-1].split(".")[0]
        name = address.split("data.")[-1].split(".")[1].split("[")[0]
    c = {"address": address, "mode": mode, "type": rtype, "name": name, "provider_name": provider,
         "change": {"actions": actions, "before": before, "after": after,
                    "after_unknown": unknown if unknown is not None else {},
                    "before_sensitive": bs if bs is not None else ({} if before is not None else False),
                    "after_sensitive": as_ if as_ is not None else ({} if after is not None else False)}}
    if module: c["module_address"] = module
    if index is not None: c["index"] = index
    if reason: c["action_reason"] = reason
    if replace_paths: c["change"]["replace_paths"] = replace_paths
    if deposed: c["deposed"] = deposed
    if previous: c["previous_address"] = previous
    if importing: c["change"]["importing"] = importing
    return c

def doc(changes, outputs=None, drift=None, errored=False, extra=None):
    d = {"format_version": "1.2", "terraform_version": "1.9.8", "planned_values": {"root_module": {}},
         "resource_changes": changes, "configuration": {"root_module": {}}, "timestamp": "2026-11-05T09:00:00Z",
         "applyable": not errored, "complete": True, "errored": errored}
    if outputs is not None: d["output_changes"] = outputs
    if drift is not None: d["resource_drift"] = drift
    if extra: d.update(extra)
    return d

def out_change(actions, before, after, unknown=False, bs=False, as_=False):
    return {"actions": actions, "before": before, "after": after, "after_unknown": unknown, "before_sensitive": bs, "after_sensitive": as_}

subnet_before = {"id": "subnet-0a1", "cidr_block": "10.0.1.0/24", "availability_zone": "eu-central-1a", "tags": {"Name": "app-a"}, "password": C}
subnet_after = {"id": "subnet-0a1", "cidr_block": "10.0.1.0/24", "availability_zone": "eu-central-1a", "tags": {"Name": "app-a", "owner": "platform"}, "password": C}

all_actions = doc([
    rc("aws_s3_bucket.logs", ["create"], None,
       {"bucket": "hclaro-logs", "region": "eu-central-1", "tags": {"env": "demo"}, "secret_access_key": C, "policy": C},
       unknown={"id": True, "arn": True}, as_={"policy": True}),
    rc("module.network.aws_subnet.main[0]", ["update"], subnet_before, subnet_after, module="module.network", index=0,
       bs={"password": True}, as_={"password": True}),
    rc("module.network.aws_subnet.main[1]", ["no-op"], subnet_before, subnet_before, module="module.network", index=1,
       bs={"password": True}, as_={"password": True}),
    rc("aws_iam_role.legacy", ["delete"], {"id": "legacy", "name": "legacy-deployer", "policy": C}, None,
       reason="delete_because_no_resource_config"),
    rc("module.compute.aws_instance.main[0]", ["delete", "create"],
       {"id": "i-0aa", "instance_type": "t3.small", "ami": "ami-old", "user_data": C, "availability_zone": "eu-central-1a"},
       {"instance_type": "t3.small", "ami": "ami-new", "user_data": C, "availability_zone": "eu-central-1a"},
       unknown={"id": True, "arn": True}, module="module.compute", index=0,
       reason="replace_because_cannot_update", replace_paths=[["ami"]], bs={"user_data": True}, as_={"user_data": True}),
    rc("module.compute.module.workers[\"blue.eu\"].aws_instance.node[\"quote\\\"key\"]", ["create", "delete"],
       {"id": "i-0bb", "instance_type": "t3.micro", "tags": {"pool": "blue"}},
       {"instance_type": "t3.micro", "tags": {"pool": "blue"}},
       unknown={"id": True}, module="module.compute.module.workers[\"blue.eu\"]", index="quote\"key",
       reason="replace_by_request"),
    rc("data.aws_caller_identity.current", ["read"], None, {"account_id": "000000000000"}, unknown={"id": True},
       mode="data", reason="read_because_config_unknown"),
    rc("aws_db_instance.primary", ["update"],
       {"id": "db-1", "engine_version": "15.4", "allocated_storage": 20, "password": C, "connection": {"host": C}},
       {"id": "db-1", "engine_version": "16.1", "allocated_storage": 40, "password": C, "connection": {"host": C}},
       bs={"password": True}, as_={"password": True}),
    rc("aws_instance.retired", ["delete"], {"id": "i-0dd", "instance_type": "t3.nano"}, None, deposed="00000001"),
    rc("aws_security_group.web", ["update"], {"id": "sg-1", "name": "web"}, {"id": "sg-1", "name": "web-public"},
       previous="aws_security_group.legacy_web"),
    rc("aws_vpc.imported", ["no-op"], {"id": "vpc-1", "cidr_block": "10.0.0.0/16"}, {"id": "vpc-1", "cidr_block": "10.0.0.0/16"},
       importing={"id": "vpc-1"}),
    rc("random_password.unused", ["forget"], {"id": "none", "result": C}, None, provider=RND, bs={"result": True}),
], outputs={
    "bucket_name": out_change(["create"], None, "hclaro-logs"),
    "db_password": out_change(["update"], C, C, bs=True, as_=True),
    "instance_ids": out_change(["update"], ["i-0aa"], None, unknown=True),
    "legacy_role": out_change(["delete"], "legacy-deployer", None),
    "region": out_change(["no-op"], "eu-central-1", "eu-central-1"),
}, drift=[
    rc("aws_s3_bucket.artifacts", ["update"], {"id": "hclaro-demo-artifacts", "tags": {"env": "demo"}, "policy": C},
       {"id": "hclaro-demo-artifacts", "tags": {"env": "demo", "changed_by": "console"}, "policy": C}),
])

# M24.01: action combinations Hclaro does not support, in a resource change and an output
# change, next to an ordinary update. Such a plan is reviewable but never applied.
unknown_actions = doc([
    rc("aws_security_group.web", ["update"], {"id": "sg-1", "name": "web"}, {"id": "sg-1", "name": "web-public"}),
    rc("aws_lb.edge", ["update", "delete"], {"id": "lb-1", "name": "edge", "token": C}, {"id": "lb-1", "name": "edge2", "token": C},
       bs={"token": True}, as_={"token": True}),
], outputs={
    "edge_name": out_change(["update", "delete"], "edge", "edge2"),
})

# PRD fixture 5: two replacements plus collateral deletion.
collateral = doc([
    rc("aws_instance.web[0]", ["create", "delete"], {"id": "i-1", "ami": "ami-a", "instance_type": "t3.small"},
       {"ami": "ami-b", "instance_type": "t3.small"}, unknown={"id": True}, index=0, reason="replace_by_request", replace_paths=[["ami"]]),
    rc("aws_instance.web[1]", ["create", "delete"], {"id": "i-2", "ami": "ami-a", "instance_type": "t3.small"},
       {"ami": "ami-b", "instance_type": "t3.small"}, unknown={"id": True}, index=1, reason="replace_by_request", replace_paths=[["ami"]]),
    rc("aws_lb_target_group_attachment.web[2]", ["delete"], {"id": "tga-3", "port": 80}, None, index=2,
       reason="delete_because_count_index"),
    rc("aws_lb.edge", ["no-op"], {"id": "lb-1", "name": "edge"}, {"id": "lb-1", "name": "edge"}),
])

no_changes = doc([rc("aws_vpc.main", ["no-op"], {"id": "vpc-1"}, {"id": "vpc-1"})], outputs={})

errored = doc([rc("aws_vpc.main", ["update"], {"id": "vpc-1", "cidr_block": "10.0.0.0/16"}, {"id": "vpc-1", "cidr_block": "10.1.0.0/16"})], errored=True)

# M4: an import with one unrelated update in the same plan.
import_plan = doc([
    rc("aws_s3_bucket.legacy_logs", ["no-op"], {"id": "hclaro-legacy-logs", "bucket": "hclaro-legacy-logs"},
       {"id": "hclaro-legacy-logs", "bucket": "hclaro-legacy-logs"}, importing={"id": "hclaro-legacy-logs"}),
    rc("aws_s3_bucket.artifacts", ["update"], {"id": "hclaro-demo-artifacts", "tags": {"env": "demo"}},
       {"id": "hclaro-demo-artifacts", "tags": {"env": "demo", "owner": "platform"}}),
], outputs={})

for name, d in [("all-actions", all_actions), ("collateral", collateral), ("no-changes", no_changes), ("errored", errored), ("import", import_plan), ("unknown-actions", unknown_actions)]:
    with open(os.path.join(out, name + ".json"), "w") as f:
        json.dump(d, f, indent=2); f.write("\n")

# Machine-readable UI stream (plan -json) for the fake engine and parser tests.
def hook(addr, mod=""):
    return {"addr": addr, "module": mod, "resource": addr.split(".", 1)[-1], "implied_provider": "aws", "resource_type": addr.split(".")[-2], "resource_name": addr.split(".")[-1], "resource_key": None}
def msg(t, m, level="info", **kw):
    d = {"@level": level, "@message": m, "@module": "terraform.ui", "@timestamp": "2026-11-05T09:00:00.000000Z", "type": t}
    d.update(kw); return d
log = [
    msg("version", "Terraform 1.9.8", terraform="1.9.8", ui="1.2"),
    msg("refresh_start", "aws_s3_bucket.artifacts: Refreshing state... [id=hclaro-demo-artifacts]", hook={"resource": hook("aws_s3_bucket.artifacts"), "id_key": "id", "id_value": "hclaro-demo-artifacts"}),
    msg("refresh_complete", "aws_s3_bucket.artifacts: Refresh complete [id=hclaro-demo-artifacts]", hook={"resource": hook("aws_s3_bucket.artifacts"), "id_key": "id", "id_value": "hclaro-demo-artifacts"}),
    msg("log", "token=" + C),
    msg("resource_drift", "aws_s3_bucket.artifacts: Drift detected (update)", change={"resource": hook("aws_s3_bucket.artifacts"), "action": "update"}),
    msg("planned_change", "aws_s3_bucket.logs: Plan to create", change={"resource": hook("aws_s3_bucket.logs"), "action": "create"}),
    msg("planned_change", "module.compute.aws_instance.main[0]: Plan to replace", change={"resource": hook("module.compute.aws_instance.main[0]", "module.compute"), "action": "replace", "reason": "cannot_update"}),
    msg("diagnostic", "Warning: Deprecated attribute", level="warn", diagnostic={"severity": "warning", "summary": "Deprecated attribute", "detail": "The attribute \"password\" is deprecated. Value was " + C + ".", "snippet": {"code": "password = \"" + C + "\""}}),
    msg("change_summary", "Plan: 2 to add, 3 to change, 4 to destroy.", changes={"add": 2, "change": 3, "import": 1, "remove": 4, "operation": "plan"}),
]
with open(os.path.join(out, "plan-log.jsonl"), "w") as f:
    for line in log: f.write(json.dumps(line) + "\n")
lock_log = [
    msg("version", "Terraform 1.9.8", terraform="1.9.8", ui="1.2"),
    msg("diagnostic", "Error: Error acquiring the state lock", level="error", diagnostic={"severity": "error", "summary": "Error acquiring the state lock", "detail": "Lock Info:\n  ID: 1234\n  Path: s3://bucket/" + C + "\n  Who: someone@host"}),
]
with open(os.path.join(out, "plan-log-locked.jsonl"), "w") as f:
    for line in lock_log: f.write(json.dumps(line) + "\n")

# Configuration a real engine would write with -generate-config-out, including a planted
# canary that the display copy must redact.
generated = """# __generated__ by Terraform
# Please review these resources and move them into your main configuration files.

# __generated__ by Terraform from "hclaro-legacy-logs"
resource "aws_s3_bucket" "legacy_logs" {
  bucket        = "hclaro-legacy-logs"
  force_destroy = false
  tags = {
    env = "demo"
  }
  replication_token = "%s"
  policy = <<EOT
{"Statement":[{"Sid":"%s"}]}
EOT
}
""" % (C, C)
with open(os.path.join(out, "import-generated.tf"), "w") as f:
    f.write(generated)
