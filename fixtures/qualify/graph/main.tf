# M27.04 G1 module-heavy project: built-in terraform_data only, default local backend.
# for_each modules over a map, a nested for_each module, count and for_each resources (keyed
# instances). retired.tf is applied, then deleted by the harness, so its objects stay in state
# as orphans; grow.tfvars then adds an instance and replaces the keyed edges.
variable "token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "replicas" {
  type    = map(number)
  default = { blue = 2, green = 1 }
}

output "token" {
  value     = var.token
  sensitive = true
}

module "app" {
  source   = "./modules/app"
  for_each = var.replicas
  name     = each.key
  replicas = each.value
}

resource "terraform_data" "edge" {
  for_each         = toset(["a", "b"])
  input            = each.key
  triggers_replace = length(module.app["blue"].ids)
}
