# M27.03 import project: hashicorp/random 3.9.1 from the offline mirror, local backend.
# I1 imports random_integer.imported (ID "<result>,<min>,<max>"); I3 imports a
# random_password without configuration and lets the engine generate it; its import ID is
# the canary. The token is kept only as a sensitive output (no resource change).
# I2 sets with_string (string.tfvars) and imports random_string.imported[0], whose import ID
# equals its state id, then tries the same ID at another address.
terraform {
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "= 3.9.1"
    }
  }
}

variable "token" {
  type      = string
  sensitive = true
}

output "token" {
  value     = var.token
  sensitive = true
}

resource "random_integer" "imported" {
  min = 1
  max = 50000
}

variable "with_string" {
  type    = bool
  default = false
}

resource "random_string" "imported" {
  count  = var.with_string ? 1 : 0
  length = 12
}
