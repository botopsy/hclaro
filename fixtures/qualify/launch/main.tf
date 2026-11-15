# M27.03 launch project: built-in terraform_data only, local backend. Variables select the
# variant a scenario plans, so no scenario edits configuration except A3 (stale plan).
# The canary is a sensitive variable from launch.tfvars, kept in state only as a sensitive
# output, which the engine marks. It is not a terraform_data input: Terraform 1.5.5 marks the
# input sensitive in state but not the output derived from it, and Hclaro shows unmarked values
# (owner decision 2026-11-15; M27.03 finding, tasks/M27-PROGRESS.md).
# No backend block and no provider: init writes nothing here, which Hclaro must still read as
# initialized (M27.03 D-1).
variable "token" {
  type      = string
  sensitive = true
}

# A2: retire = true deletes terraform_data.retired; generation changes replace rotated.
variable "retire" {
  type    = bool
  default = false
}

variable "generation" {
  type    = number
  default = 1
}

# R1: fleet_size instances to select for -replace.
variable "fleet_size" {
  type    = number
  default = 0
}

output "token" {
  value     = var.token
  sensitive = true
}

resource "terraform_data" "kept" {
  input = "kept"
}

resource "terraform_data" "rotated" {
  triggers_replace = var.generation
}

resource "terraform_data" "retired" {
  count = var.retire ? 0 : 1
  input = "retired"
}

resource "terraform_data" "fleet" {
  count = var.fleet_size
  input = count.index
}
