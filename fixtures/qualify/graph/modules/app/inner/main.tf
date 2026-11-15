variable "name" {
  type = string
}

resource "terraform_data" "leaf" {
  input = var.name
}
