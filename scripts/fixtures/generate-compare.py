#!/usr/bin/env python3
"""Regenerate the synthetic workspace comparison fixtures in fixtures/compare.

Every document is an explicitly synthetic `show -json` state. Nothing describes real
infrastructure. DO_NOT_LEAK_HCLARO_TEST_SECRET is planted in marked, risky-named and
ordinary-named attributes; it must never appear in a comparison response or report.

  dev.json           Terraform, workspace "dev"
  prod.json          Terraform, workspace "prod"
  prod-tofu.json     prod as recorded by OpenTofu (registry.opentofu.org providers)
  prod-renamed.json  prod with module.app renamed to module.application
"""
import json
import os

CANARY = "DO_NOT_LEAK_HCLARO_TEST_SECRET"
OUT = os.path.join(os.path.dirname(__file__), "..", "..", "fixtures", "compare")


def resource(address, mode, rtype, name, values, provider, index=None, sensitive=None):
    r = {
        "address": address,
        "mode": mode,
        "type": rtype,
        "name": name,
        "provider_name": provider,
        "schema_version": 0,
        "values": values,
        "sensitive_values": sensitive or {},
    }
    if index is not None:
        r["index"] = index
    return r


def state(env, registry="registry.terraform.io", app_module="module.app", engine="terraform"):
    aws = registry + "/hashicorp/aws"
    region = {"dev": "us-east-1", "prod": "eu-west-1"}[env]
    cidr = {"dev": "10.0.0.0/16", "prod": "10.1.0.0/16"}[env]
    root = [
        resource("aws_vpc.main", "managed", "aws_vpc", "main", {
            "id": f"vpc-{env}main",
            "cidr_block": cidr,
            "enable_dns_support": True,
            "tags": {"Environment": env, "Team": "platform"},
        }, aws),
        resource("aws_s3_bucket.logs", "managed", "aws_s3_bucket", "logs", {
            "id": f"hclaro-{env}-logs",
            "bucket": f"hclaro-{env}-logs",
            "region": region,
            "force_destroy": False,
            "tags": {"Environment": env},
        }, aws),
        resource("aws_db_instance.main", "managed", "aws_db_instance", "main", {
            "id": f"{env}-db",
            "engine": "postgres",
            "engine_version": "16.3",
            "password": CANARY,
            "master_user_secret": [{"secret_arn": CANARY}],
            "description": CANARY,
        }, aws, sensitive={"password": True, "master_user_secret": [{"secret_arn": True}]}),
        resource("aws_sns_topic.alerts", "managed", "aws_sns_topic", "alerts", {
            "id": f"{env}-alerts",
            "name": f"{env}-alerts",
        }, (registry + "/acme/aws") if env == "prod" else aws),
        resource("data.aws_ami.ubuntu", "data", "aws_ami", "ubuntu", {
            "id": "ami-ubuntu-2404",
            "name": "ubuntu-24.04",
            "most_recent": True,
        }, aws),
    ]
    if env == "dev":
        root.append(resource("aws_iam_role.debug", "managed", "aws_iam_role", "debug", {
            "id": "dev-debug",
            "name": "dev-debug",
        }, aws))
    if env == "prod":
        root.append(resource("data.aws_caller_identity.current", "data", "aws_caller_identity", "current", {
            "id": "synthetic-account",
            "arn": "arn:aws:iam::000000000000:user/synthetic",
        }, aws))
    instances = 2 if env == "dev" else 3
    app = [
        resource(f"{app_module}.aws_instance.web[{i}]", "managed", "aws_instance", "web", {
            "id": f"i-{env}web{i}",
            "ami": "ami-ubuntu-2404",
            "instance_type": "t3.small" if env == "dev" else "m6i.large",
            "user_data": CANARY,
            "tags": {"Name": f"{env}-web-{i}"},
        }, aws, index=i)
        for i in range(instances)
    ]
    app.append(resource(f"{app_module}.aws_lb.front", "managed", "aws_lb", "front", {
        "id": f"{env}-front",
        "name": f"{env}-front",
        "internal": False,
        "subnets": ["subnet-a", "subnet-b"],
    }, aws))
    monitoring = [
        resource('module.monitoring.aws_cloudwatch_metric_alarm.cpu["api"]', "managed", "aws_cloudwatch_metric_alarm", "cpu", {
            "id": f"{env}-api-cpu",
            "alarm_name": f"{env}-api-cpu",
            "threshold": 80 if env == "dev" else 70,
        }, aws, index="api"),
        resource('module.monitoring.aws_cloudwatch_metric_alarm.cpu["worker.batch"]', "managed", "aws_cloudwatch_metric_alarm", "cpu", {
            "id": f"{env}-worker-cpu",
            "alarm_name": f"{env}-worker-cpu",
            "threshold": 90,
        }, aws, index="worker.batch"),
    ]
    doc = {
        "format_version": "1.0",
        "values": {
            "root_module": {
                "resources": root,
                "child_modules": [
                    {"address": app_module, "resources": app},
                    {"address": "module.monitoring", "resources": monitoring},
                ],
            }
        },
    }
    doc["terraform_version"] = "1.9.8" if engine == "terraform" else "1.8.3"
    return doc


def write(name, doc):
    with open(os.path.join(OUT, name), "w") as f:
        json.dump(doc, f, indent=2)
        f.write("\n")


if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    write("dev.json", state("dev"))
    write("prod.json", state("prod"))
    write("prod-tofu.json", state("prod", registry="registry.opentofu.org", engine="tofu"))
    write("prod-renamed.json", state("prod", app_module="module.application"))
