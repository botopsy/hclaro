# M27.05 Hub project: built-in terraform_data, hashicorp/random 3.9.1 and hashicorp/local
# 2.9.1 from the offline mirror, local backend. hold.tfvars sets hold = true: a plan (or an
# import plan) then reads data.local_file.gate from the named pipe .qual/gate.fifo that the
# harness creates, so the operation stays running until the harness writes the pipe (H4, H5,
# H7, H9, H11). No local-exec; nothing outside the project is read or written.
# random_integer.imported is the H11 import target (ID "<result>,<min>,<max>").
terraform {
  backend "local" {}
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "= 3.9.1"
    }
    local = {
      source  = "hashicorp/local"
      version = "= 2.9.1"
    }
  }
}

variable "token" {
  type      = string
  sensitive = true
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

resource "random_integer" "imported" {
  min = 1
  max = 50000
}

data "local_file" "gate" {
  count    = var.hold ? 1 : 0
  filename = "${path.module}/.qual/gate.fifo"
}
