# M27.03 A5 project: built-in terraform_data plus hashicorp/local 2.9.1 from the offline
# mirror, local backend. Each var file sets a generation, so each profile's plan replaces
# terraform_data.rotated. data.local_file.gate exists only when TF_VAR_hold=true, which only
# the F-2 plan lock holder (Terraform 1.17.2) receives: it blocks on .qual/gate.fifo while
# holding the lock. Hclaro's own plans never read it.
terraform {
  backend "local" {}
  required_providers {
    local = {
      source  = "hashicorp/local"
      version = "= 2.9.1"
    }
  }
}

# The default lets the plan holder evaluate the configuration without the token (the
# harness refuses credential-like TF_VAR_ names); Hclaro's plans set it from the var files.
variable "token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "generation" {
  type    = number
  default = 1
}

variable "hold" {
  type    = bool
  default = false
}

output "token" {
  value     = var.token
  sensitive = true
}

resource "terraform_data" "kept" {
  input = "kept"
}

resource "terraform_data" "rotated" {
  triggers_replace = var.generation
}

data "local_file" "gate" {
  count    = var.hold ? 1 : 0
  filename = "${path.module}/.qual/gate.fifo"
}
