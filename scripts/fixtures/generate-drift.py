# Run: python3 -I scripts/fixtures/generate-drift.py fixtures/drift
# Generates synthetic refresh-only plan JSON fixtures (format 1.2) and `plan -refresh-only -json`
# UI streams for Hclaro drift scan tests. Nothing here came from a real account.
import json, sys, os
out = sys.argv[1]
os.makedirs(out, exist_ok=True)
C = "DO_NOT_LEAK_HCLARO_TEST_SECRET"
# Credential-shaped, under an ordinary attribute name, and NOT flagged sensitive by the
# "provider": redaction must still hide it.
AKID = "AKIAABCDEFGHIJKLMNOP"
AWS = "registry.terraform.io/hashicorp/aws"

def rc(address, actions, before, after, unknown=None, bs=None, as_=None, mode="managed", module=None, index=None, provider=AWS):
    tail = address.split(".")
    rtype, name = tail[-2], tail[-1].split("[")[0]
    if mode == "data":
        rtype, name = address.split("data.")[-1].split(".")[0], address.split("data.")[-1].split(".")[1]
    c = {"address": address, "mode": mode, "type": rtype, "name": name, "provider_name": provider,
         "change": {"actions": actions, "before": before, "after": after,
                    "after_unknown": unknown if unknown is not None else {},
                    "before_sensitive": bs if bs is not None else ({} if before is not None else False),
                    "after_sensitive": as_ if as_ is not None else ({} if after is not None else False)}}
    if module: c["module_address"] = module
    if index is not None: c["index"] = index
    return c

def doc(drift, errored=False, version="1.9.8", changes=None):
    return {"format_version": "1.2", "terraform_version": version, "planned_values": {"root_module": {}},
            "resource_drift": drift, "resource_changes": changes or [], "configuration": {"root_module": {}},
            "timestamp": "2026-11-06T09:00:00Z", "applyable": False, "complete": not errored, "errored": errored}

bucket_before = {"id": "hclaro-demo-artifacts", "bucket": "hclaro-demo-artifacts", "region": "eu-central-1",
                 "secret_access_key": C, "tags": {"environment": "demo"}}
bucket_after = dict(bucket_before, tags={"environment": "demo", "owner": "someone-in-console"})
changed_tag = rc("aws_s3_bucket.artifacts", ["update"], bucket_before, bucket_after,
                 bs={"secret_access_key": True}, as_={"secret_access_key": True})

sg_before = {"id": "sg-0abc", "name": "web", "description": "web ingress",
             "ingress": [{"from_port": 443, "to_port": 443, "protocol": "tcp", "cidr_blocks": ["10.0.0.0/16"], "description": "https"}],
             "tags": {"Name": "web"}}
sg_after = {"id": "sg-0abc", "name": "web", "description": AKID,
            "ingress": [{"from_port": 443, "to_port": 443, "protocol": "tcp", "cidr_blocks": ["10.0.0.0/16"], "description": "https"},
                        {"from_port": 22, "to_port": 22, "protocol": "tcp", "cidr_blocks": ["0.0.0.0/0"], "description": "debug " + C}],
            "tags": {"Name": "web"}}
security_group = rc("aws_security_group.web", ["update"], sg_before, sg_after)

instance = {"id": "i-0demo3", "instance_type": "t3.small", "availability_zone": "eu-central-1a", "password": C, "tags": {"Name": "compute-3"}}
deleted = rc("module.compute.aws_instance.main[3]", ["delete"], instance, None, module="module.compute", index=3,
             bs={"password": True})

subnet = {"id": "subnet-00", "availability_zone": "eu-central-1a", "tags": {"Name": "net-0"}, "map_public_ip_on_launch": False}
unknown = rc("module.network.aws_subnet.main[0]", ["update"], subnet, dict(subnet, map_public_ip_on_launch=None),
             unknown={"map_public_ip_on_launch": True}, module="module.network", index=0)

caller = rc("data.aws_caller_identity.current", ["read"], {"id": "000000000000"}, {"id": "000000000000"}, mode="data")

fixtures = {
    "no-drift": doc([]),
    "changed-tag": doc([changed_tag]),
    "security-group": doc([security_group]),
    "deleted": doc([deleted]),
    "unknown": doc([unknown]),
    # Partial provider failure: one object was refreshed and drifted, another could not be read.
    "inaccessible": doc([changed_tag], errored=True),
    "mixed": doc([changed_tag, security_group, deleted, unknown, caller]),
    "mixed-tofu": doc([changed_tag, deleted], version="1.8.3"),
    # A document that proposes changes is not a refresh-only result and must be refused.
    "not-refresh-only": doc([], changes=[rc("aws_s3_bucket.artifacts", ["update"], bucket_before, bucket_after,
                                            bs={"secret_access_key": True}, as_={"secret_access_key": True})]),
}
for name, d in fixtures.items():
    with open(os.path.join(out, name + ".json"), "w") as f:
        json.dump(d, f, indent=2); f.write("\n")

def hook(addr):
    return {"resource": {"addr": addr, "module": "", "resource": addr.split(".")[-1], "implied_provider": "aws",
                         "resource_type": addr.split(".")[-2], "resource_name": addr.split(".")[-1], "resource_key": None},
            "id_key": "id", "id_value": C}
def msg(t, m, level="info", **kw):
    return dict({"@level": level, "@message": m, "@module": "terraform.ui", "@timestamp": "2026-11-06T09:00:00.000000Z", "type": t}, **kw)
version = msg("version", "Terraform 1.9.8", terraform="1.9.8", ui="1.2")
def refresh(addr):
    return [msg("refresh_start", addr + ": Refreshing state... [id=" + C + "]", hook=hook(addr)),
            msg("refresh_complete", addr + ": Refresh complete [id=" + C + "]", hook=hook(addr))]
def drifted(addr, action):
    return msg("resource_drift", addr + ": Drift detected (" + action + ")", change={"resource": hook(addr)["resource"], "action": action})
def diag(summary, detail, address=None):
    d = {"severity": "error", "summary": summary, "detail": detail}
    if address: d["address"] = address
    return msg("diagnostic", "Error: " + summary, level="error", diagnostic=d)

logs = {
    "log": [version, *refresh("aws_s3_bucket.artifacts"), *refresh("aws_security_group.web"),
            drifted("aws_s3_bucket.artifacts", "update"), drifted("aws_security_group.web", "update"),
            drifted("module.compute.aws_instance.main[3]", "delete"),
            msg("log", "secret_access_key=" + C),
            msg("change_summary", "Plan: 0 to add, 0 to change, 0 to destroy.", changes={"add": 0, "change": 0, "import": 0, "remove": 0, "operation": "plan"})],
    "log-denied": [version, *refresh("aws_s3_bucket.artifacts"), drifted("aws_s3_bucket.artifacts", "update"),
                   diag("reading IAM Role (demo-deployer): AccessDenied", "User is not authorized to perform iam:GetRole with token " + C, "aws_iam_role.deployer")],
    "log-sso": [version, diag("No valid credential sources found", "The SSO session associated with this profile has expired or is otherwise invalid. Session " + C)],
    "log-locked": [version, diag("Error acquiring the state lock", "Lock Info:\n  ID: 1234\n  Path: s3://bucket/" + C + "\n  Who: someone@host")],
}
for name, lines in logs.items():
    with open(os.path.join(out, name + ".jsonl"), "w") as f:
        for line in lines: f.write(json.dumps(line) + "\n")
