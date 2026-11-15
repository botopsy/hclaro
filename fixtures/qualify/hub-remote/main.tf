# M27.05 H12 only: a backend block that is never reached. backend "s3" has no settings and
# the project is never initialized, so no engine command can contact it. The fixture guard
# rejects it; H12 records the exception for this file alone (ProjectRemote). Runs only under
# enforced isolation.
terraform {
  backend "s3" {}
}

resource "terraform_data" "kept" {
  input = "kept"
}
