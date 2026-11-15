# Recording project for real drift and failure diagnostics (M27.04): hashicorp/local 2.9.1 from
# the offline M27.01 mirror, default local backend, a hand-written synthetic state; never
# applied. See scripts/fixtures/record-engine.sh. The recorder deletes out/gone.txt and edits
# out/edited.txt before the refresh-only plan; out/same.txt matches the state.
terraform {
  required_providers {
    local = {
      source  = "hashicorp/local"
      version = "= 2.9.1"
    }
  }
}

# missing.tfvars points this at a file that does not exist (plan_failed).
variable "config_path" {
  type    = string
  default = ""
}

resource "terraform_data" "config" {
  input = var.config_path == "" ? "none" : file(var.config_path)
}

resource "local_file" "gone" {
  filename = "out/gone.txt"
  content  = "gone\n"
}

resource "local_file" "edited" {
  filename = "out/edited.txt"
  content  = "original\n"
}

resource "local_file" "same" {
  filename = "out/same.txt"
  content  = "same\n"
}
