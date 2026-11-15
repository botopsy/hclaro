variable "name" {
  type = string
}

variable "replicas" {
  type = number
}

resource "terraform_data" "unit" {
  count = var.replicas
  input = "${var.name}-${count.index}"
}

module "inner" {
  source   = "./inner"
  for_each = toset(["x", "y"])
  name     = "${var.name}-${each.key}"
}

output "ids" {
  value = terraform_data.unit[*].id
}
