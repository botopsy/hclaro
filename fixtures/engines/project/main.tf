# Recording project for real-engine goldens (M26.16). Built-in resources only, default local
# backend, hand-written synthetic state; never applied. See scripts/fixtures/record-engine.sh.

variable "token" {
  type      = string
  sensitive = true
}

resource "terraform_data" "updated" {
  input = "v2"
}

resource "terraform_data" "unchanged" {
  input = "same"
}

resource "terraform_data" "created" {
  count = 2
  input = "item-${count.index}"
}

resource "terraform_data" "secret" {
  input = var.token
}

resource "terraform_data" "replaced" {
  input            = "x"
  triggers_replace = ["b"]
}

resource "terraform_data" "replaced_first" {
  input            = "y"
  triggers_replace = ["b"]

  lifecycle {
    create_before_destroy = true
  }
}

resource "terraform_data" "renamed" {
  input = "kept"
}

moved {
  from = terraform_data.old_name
  to   = terraform_data.renamed
}

module "group" {
  source   = "./modules/group"
  for_each = toset(["a", "b"])
  name     = each.key
}

data "terraform_remote_state" "pending" {
  backend    = "local"
  config     = { path = "remote.json" }
  depends_on = [terraform_data.created]
}

output "ids" {
  value = terraform_data.created[*].id
}

output "label" {
  value = "new"
}

output "token" {
  value     = var.token
  sensitive = true
}
