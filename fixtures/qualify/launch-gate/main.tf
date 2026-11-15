# M27.03 A6, A7, A7b gated fixture (OD-8, approved with restrictions 2026-11-15; only these
# scenarios). Built-in terraform_data only, implicit local backend. Stage 1 objects, then a
# gate whose local-exec runs the fixed script ./gate.sh (Unix /bin/sh), which only touches
# <project>/.qual/ and polls for .qual/release with a hard timeout. Stage 2 depends on the gate.
# The harness guard refuses any provisioner unless this file and gate.sh are byte-identical
# to the approved fixture.
variable "token" {
  type      = string
  sensitive = true
}

# normal: the INT trap records the signal and exits 130 (timeout 120 s).
# trap: the INT trap records the signal and keeps polling (timeout 900 s > grace period).
variable "gate_mode" {
  type    = string
  default = "normal"
  validation {
    condition     = contains(["normal", "trap"], var.gate_mode)
    error_message = "gate_mode must be normal or trap."
  }
}

output "token" {
  value     = var.token
  sensitive = true
}

resource "terraform_data" "stage1" {
  count = 3
  input = count.index
}

resource "terraform_data" "gate" {
  depends_on = [terraform_data.stage1]

  provisioner "local-exec" {
    command     = "/bin/sh ./gate.sh ${var.gate_mode}"
    working_dir = path.module
  }
}

resource "terraform_data" "stage2" {
  count      = 2
  depends_on = [terraform_data.gate]
  input      = count.index
}
