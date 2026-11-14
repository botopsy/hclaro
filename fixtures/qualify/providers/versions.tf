# M27 provider pins (OD-2, 2026-11-14). Input to mirror-providers.sh and verify-mirror.sh.
# The fixture declares no resources: init only installs providers.
terraform {
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "= 3.9.1"
    }
    local = {
      source  = "hashicorp/local"
      version = "= 2.9.1"
    }
  }
}
