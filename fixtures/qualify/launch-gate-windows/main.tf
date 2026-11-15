# M27.07 WIN1: the Windows variant of the M27.03 A6, A7, A7b gated fixture (OD-8, approved with
# restrictions 2026-11-15; PowerShell gate approved by the owner for WIN1, 2026-11-15). Built-in
# terraform_data only, implicit local backend. Stage 1 objects, then a gate whose local-exec runs
# the fixed script ./gate.ps1 with Windows PowerShell, which only touches <project>/.qual/ and
# polls for .qual/release with a hard timeout. Stage 2 depends on the gate. The harness guard
# refuses any provisioner unless this file and gate.ps1 are byte-identical to the approved fixture.
variable "token" {
  type      = string
  sensitive = true
}

# normal: the interrupt handler records the event and exits 130 (timeout 120 s).
# trap: the handler records the event and the gate keeps polling (timeout 900 s > grace period).
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
    interpreter = ["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"]
    command     = "./gate.ps1"
    working_dir = path.module
    environment = {
      GATE_MODE = var.gate_mode
    }
  }
}

resource "terraform_data" "stage2" {
  count      = 2
  depends_on = [terraform_data.gate]
  input      = count.index
}
