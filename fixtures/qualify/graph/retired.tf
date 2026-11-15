# Deleted by the G1 scenario after the first apply: these objects become orphans in state.
resource "terraform_data" "orphan" {
  count = 2
  input = "orphan-${count.index}"
}

module "old" {
  source = "./modules/app/inner"
  name   = "old"
}
