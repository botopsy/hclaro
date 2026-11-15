# M27.02 harness smoke project for the fake engine (scripts/fake-engine). Synthetic; the fake
# engine ignores the configuration. The canary is planted in a sensitive variable's var file.
variable "token" {
  type      = string
  sensitive = true
}

resource "terraform_data" "smoke" {
  input = var.token
}
