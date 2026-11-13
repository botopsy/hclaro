variable "name" {
  type = string
}

resource "terraform_data" "this" {
  input = "group-${var.name}-v2"
}
