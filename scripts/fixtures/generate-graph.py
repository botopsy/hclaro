#!/usr/bin/env python3
"""Generate the synthetic `graph` fixtures in fixtures/graph.

Nothing here comes from a real project. The dependency story matches fixtures/state.json and
fixtures/plan/all-actions.json so the UI can show potential impact next to planned changes:

- terraform.dot: the resource-only format of Terraform 1.7+ (rankdir, module clusters).
- tofu.dot: the full walk graph of Terraform 1.5/1.6 and OpenTofu, with [root] prefixes,
  provider/variable/local/output/module nodes, expand and close suffixes and the root node.
- large.dot: 1,000 resource nodes in 20 modules, for the size and usability tests.

Run from the repository root: python3 scripts/fixtures/generate-graph.py
"""
import random

AWS = 'provider[\\"registry.terraform.io/hashicorp/aws\\"]'
RANDOM = 'provider[\\"registry.terraform.io/hashicorp/random\\"]'

# address -> list of addresses it depends on (resource to resource).
DEPS = {
    "data.aws_caller_identity.current": [],
    "aws_iam_role.deployer": [],
    "aws_iam_role.legacy": [],
    "aws_vpc.imported": [],
    "aws_s3_bucket.artifacts": ["data.aws_caller_identity.current"],
    "aws_s3_bucket.logs": ["data.aws_caller_identity.current"],
    "aws_security_group.web": ["aws_vpc.imported"],
    "aws_instance.retired": ["aws_security_group.web"],
    "aws_db_instance.primary": ["aws_security_group.web", "module.network.aws_subnet.main"],
    "aws_lb.edge": ["aws_security_group.web", "module.network.aws_subnet.main"],
    "random_password.unused": [],
    "module.network.aws_subnet.main": ["aws_vpc.imported"],
    "module.compute.aws_instance.main": ["module.network.aws_subnet.main", "aws_security_group.web", "aws_iam_role.deployer"],
    "module.compute.module.workers.aws_instance.node": ["module.compute.aws_instance.main"],
    "module.database.aws_db_instance.main": ["module.network.aws_subnet.main"],
    "module.edge.aws_lb.main": ["module.compute.aws_instance.main", "aws_lb.edge"],
    "module.observability.aws_cloudwatch_log_group.main": ["aws_s3_bucket.logs"],
    "module.storage.aws_s3_bucket.main": ["aws_iam_role.deployer", "data.aws_caller_identity.current"],
}


def module_of(address):
    parts = address.split(".")
    mods = []
    while parts[0] == "module":
        mods.append("module." + parts[1])
        parts = parts[2:]
    return ".".join(mods), ".".join(parts)


def resources_format(deps, header="// Synthetic Terraform 1.9 `graph` output for Hclaro tests.\n"):
    out = [header, "digraph G {\n", '  rankdir = "RL";\n', '  node [shape = rect, fontname = "sans-serif"];\n']
    clusters = {}
    for address in deps:
        module, local = module_of(address)
        clusters.setdefault(module, []).append((address, local))
    for address, local in clusters.pop("", []):
        out.append(f'  "{address}" [label="{local}"];\n')
    for module in sorted(clusters):
        out.append(f'  subgraph "cluster_{module}" {{\n    label = "{module}"\n    fontname = "sans-serif"\n')
        for address, local in clusters[module]:
            out.append(f'    "{address}" [label="{local}"];\n')
        out.append("  }\n")
    for address, targets in deps.items():
        for target in targets:
            out.append(f'  "{address}" -> "{target}";\n')
    out.append("}\n")
    return "".join(out)


def full_format(deps):
    """The walk graph: resources reach other modules through module variables and outputs."""
    nodes = {}
    edges = []

    def node(name, shape="box"):
        nodes.setdefault(name, shape)
        return name

    def edge(a, b):
        edges.append((node(a), node(b)))

    modules = sorted({module_of(a)[0] for a in deps} - {""})
    # Parent module paths too (module.compute for module.compute.module.workers).
    for m in list(modules):
        parts = m.split(".")
        for i in range(2, len(parts), 2):
            if ".".join(parts[:i]) not in modules:
                modules.append(".".join(parts[:i]))
    for m in sorted(modules):
        node(f"[root] {m} (expand)", "box")
        edge(f"[root] {m} (close)", f"[root] {m} (expand)")
        parent = m.rsplit(".module.", 1)[0] if ".module." in m else ""
        if parent:
            edge(f"[root] {m} (expand)", f"[root] {parent} (expand)")
    node("[root] var.region", "note")
    node(f"[root] {AWS}", "diamond")
    node(f"[root] {RANDOM}", "diamond")
    edge(f"[root] {AWS}", "[root] var.region")
    node("[root] local.tags", "note")
    for address, targets in deps.items():
        module, local = module_of(address)
        me = f"[root] {address} (expand)"
        provider = RANDOM if address.startswith("random_") else AWS
        edge(me, f"[root] {provider}")
        if module:
            edge(me, f"[root] {module} (expand)")
            edge(f"[root] {module} (close)", me)
        else:
            edge(me, "[root] local.tags")
        for target in targets:
            tmodule, _ = module_of(target)
            them = f"[root] {target} (expand)"
            if tmodule == module:
                edge(me, them)
                continue
            # Cross-module references go through an output of the target module and, inside a
            # child module, through one of its input variables.
            via = them
            if tmodule:
                out = f"[root] {tmodule}.output.{target.rsplit('.', 1)[0].rsplit('.', 1)[-1]}_id (expand)"
                edge(out, them)
                via = out
            if module:
                var = f"[root] {module}.var.{target.split('.')[-2]}_id (expand)"
                edge(var, via)
                edge(var, f"[root] {module} (expand)")
                via = var
            edge(me, via)
        edge(f"[root] {provider} (close)", me)
    edge("[root] meta.count-boundary (EachMode fixup)", "[root] aws_s3_bucket.artifacts (expand)")
    edge("[root] output.bucket_arn", "[root] aws_s3_bucket.artifacts (expand)")
    for name in [f"[root] {AWS} (close)", f"[root] {RANDOM} (close)", "[root] meta.count-boundary (EachMode fixup)"] + [f"[root] {m} (close)" for m in sorted(modules)]:
        edge("[root] root", name)
    out = ["// Synthetic OpenTofu 1.8 `graph` output (full walk graph) for Hclaro tests.\n", "digraph {\n", '\tcompound = "true"\n', '\tnewrank = "true"\n', '\tsubgraph "root" {\n']
    for name, shape in nodes.items():
        label = name.removeprefix("[root] ")
        for suffix in (" (expand)", " (close)"):
            label = label.removesuffix(suffix)
        out.append(f'\t\t"{name}" [label = "{label}", shape = "{shape}"]\n')
    seen = set()
    for a, b in edges:
        if (a, b) in seen:
            continue
        seen.add((a, b))
        out.append(f'\t\t"{a}" -> "{b}"\n')
    out.append("\t}\n}\n")
    return "".join(out)


def large():
    rnd = random.Random(11)
    deps = {}
    names = []
    for m in range(20):
        for r in range(50):
            address = f"module.m{m:02d}.aws_instance.r{r:03d}"
            pool = names[-120:]
            deps[address] = sorted(set(rnd.sample(pool, min(len(pool), rnd.randint(0, 3)))))
            names.append(address)
    return resources_format(deps, "// Synthetic 1,000-node Terraform graph for Hclaro size tests.\n")


if __name__ == "__main__":
    with open("fixtures/graph/terraform.dot", "w", newline="\n") as f:
        f.write(resources_format(DEPS))
    with open("fixtures/graph/tofu.dot", "w", newline="\n") as f:
        f.write(full_format(DEPS))
    with open("fixtures/graph/large.dot", "w", newline="\n") as f:
        f.write(large())
