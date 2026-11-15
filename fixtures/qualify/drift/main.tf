# M27.04 D1-D4: hashicorp/local 2.9.1 from the offline mirror, default local backend. Hclaro
# applies it through the reviewed-plan path; the harness then deletes or edits the files under
# out/ (file operations, not engine calls) and asks Hclaro for a drift scan.
terraform {
  required_providers {
    local = {
      source  = "hashicorp/local"
      version = "= 2.9.1"
    }
  }
}

variable "token" {
  type      = string
  sensitive = true
  default   = ""
}

output "token" {
  value     = var.token
  sensitive = true
}

resource "terraform_data" "kept" {
  input = "kept"
}

resource "local_file" "gone" {
  filename = "${path.module}/out/gone.txt"
  content  = "gone\n"
}

resource "local_file" "edited" {
  filename = "${path.module}/out/edited.txt"
  content  = "original\n"
}
