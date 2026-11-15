variable "name" {
  type = string
}

resource "terraform_data" "this" {
  input = var.name
}
