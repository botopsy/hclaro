# M27.02 reconstruction of the RE-01 project (docs/qualification/terraform-1.5.5-local.md):
# built-in terraform_data and terraform_remote_state only, default local backend, a synthetic
# recorded state. Expected plan with prod.tfvars: 13 changes = 5 create, 2 update,
# 2 replace (delete,create and create,delete), 1 delete, 1 read (dependency pending),
# 2 no-op (one is the move). Nothing here is applied by the RE-01 scenario.

variable "db_password" {
  type      = string
  sensitive = true
}

variable "release" {
  type = string
}

variable "replicas" {
  type    = number
  default = 2
}

# update
resource "terraform_data" "config" {
  input = "release-${var.release}"
}

# update of a sensitive input
resource "terraform_data" "secret" {
  input = var.db_password
}

# no-op
resource "terraform_data" "stable" {
  input = "unchanged"
}

# no-op after a move
moved {
  from = terraform_data.legacy
  to   = terraform_data.renamed
}

resource "terraform_data" "renamed" {
  input = "kept"
}

# replace, delete then create
resource "terraform_data" "rotated" {
  triggers_replace = var.release
}

# replace, create then delete
resource "terraform_data" "rolling" {
  triggers_replace = var.release
  lifecycle {
    create_before_destroy = true
  }
}

# creates: count, module for_each and one standalone
resource "terraform_data" "workers" {
  count = var.replicas
  input = "worker-${count.index}"
}

module "svc" {
  source   = "./modules/svc"
  for_each = toset(["api", "web"])
  name     = each.key
}

resource "terraform_data" "fresh" {
  input = "new"
}

# deferred read: the dependency has pending changes
data "terraform_remote_state" "upstream" {
  backend    = "local"
  config     = { path = "${path.module}/upstream.tfstate" }
  depends_on = [terraform_data.fresh]
}

output "db_password" {
  value     = var.db_password
  sensitive = true
}

output "worker_ids" {
  value = terraform_data.workers[*].id
}
