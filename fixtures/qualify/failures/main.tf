# M27.04 F1-F3 and F5: hashicorp/local 2.9.1 is required (so a missing install or a changed
# lock file needs init) but no local resource is planned; default local backend; nothing here
# is applied. missing.tfvars makes file() read a path that does not exist (plan_failed);
# big.tfvars plans big_count objects with a 1 MiB input each (plan_too_large, > 64 MiB JSON).
terraform {
  required_providers {
    local = {
      source  = "hashicorp/local"
      version = "= 2.9.1"
    }
  }
}

variable "token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "config_path" {
  type    = string
  default = ""
}

variable "big_count" {
  type    = number
  default = 0
}

output "token" {
  value     = var.token
  sensitive = true
}

locals {
  # range() returns at most 1024 elements: 16 x 64 B = 1 KiB, 1024 x 1 KiB = 1 MiB.
  kib  = join("", [for i in range(16) : "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"])
  blob = join("", [for i in range(1024) : local.kib])
}

resource "terraform_data" "config" {
  input = var.config_path == "" ? "none" : file(var.config_path)
}

resource "terraform_data" "big" {
  count = var.big_count
  input = "${count.index}-${local.blob}"
}
