# M27.04 F4: built-in terraform_data, default local backend. Once applied, raising generation
# forces a replacement of an object whose lifecycle forbids destroying it; Terraform 1.17.2 and
# OpenTofu 1.13.9 plan that as ["forget","create"], which Hclaro does not support (2026-11-15
# probe). On an engine that rejects lifecycle destroy, F4 records the engine's diagnostic.
variable "token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "generation" {
  type    = number
  default = 1
}

output "token" {
  value     = var.token
  sensitive = true
}

resource "terraform_data" "kept" {
  triggers_replace = var.generation

  lifecycle {
    destroy = false
  }
}
