# keep01 an ordinary comment
resource "aws_ssm_parameter" "db_password" {
  name  = "leak70"
  type  = "leak71"
  value = "leak01"
  tags = {
    Owner = "leak02"
  }
}

resource "aws_kms_key" "secrets" {
  description = "leak03"
  key_usage   = "leak72"
  provisioner "local-exec" {
    command = "leak04"
  }
}

module "secrets" {
  source      = "leak40"
  region      = "leak41"
  db_pass     = "leak42"
  db_password = "leak05"
}

resource "aws_db_instance" "main" {
  identifier = "keep07"
  tags       = { token = "leak06", Name = "x" }
  master_password = {
    value = "leak07"
  }
  passwords = [
    "leak08", 1234
  ]
  environment = [
    {
      value = <<EOT
leak09
EOT
      name  = "DB_PASSWORD"
    },
    { name = "LOG_LEVEL", value = "keep08" },
  ]
  password = var.a +
    "leak10"
  api_token = var.b
    ? "leak11" : "x"
  settings = { "p\u0061ssword" = "leak12" }
  env2     = [{ name = "DB_P\u0041SSWORD", value = "leak13" }]
  # password = "leak14"
  /*
  passwords = [
    "leak15",
  ]
  */
  engine = "keep09"
}

variable "db_password" {
  default = "leak16"
}

output "api_token" {
  value = "leak17"
}

variable "region" {
  default = "keep10"
}

resource "aws_db_instance" "replica" {
  engine = "keep11" // a trailing comment is not an operator
  master_password {
    value = "leak30"
  }
  port = -1
  admin_password {
    value = "leak31"
  }
  password = "a" + # a comment after the operator
    "leak32"
  identifier = "keep12"
}

resource "aws_kms_ciphertext" "db_password" {
  key_id    = "leak73"
  plaintext = "leak33"
}

resource "terraform_data" "secret" {
  input = "leak34"
}

data "external" "token" {
  program = ["leak35"]
  query   = { a = "leak36" }
}

# Blocks whose secret-like label hides every value: module, variable, output, dynamic,
# check and nested blocks.
module "db_credentials" {
  source  = "leak43"
  name    = "leak44"
  settings = {
    size = "leak45"
  }
}

variable "api_key" {
  type        = string
  description = "leak46"
  default     = "leak47"
}

output "db_secret" {
  value = aws_db_instance.main.endpoint
  description = "leak48"
}

resource "aws_lb_listener" "web" {
  port = "keep14"
  dynamic "secret" {
    for_each = var.list
    content {
      name = "leak49"
    }
  }
  token_config {
    mode = "leak50"
  }
}

check "secret_rotation" {
  assert {
    condition     = true
    error_message = "leak51"
  }
}

# A resource whose label looks like a secret is hidden whole, connection settings included.
resource "null_resource" "rotate_password" {
  connection {
    type                = "leak74"
    host                = "leak75"
    user                = "leak76"
    bastion_host        = "leak77"
    bastion_user        = "leak78"
    password            = "leak52"
    private_key         = "leak53"
    bastion_password    = "leak54"
    bastion_private_key = "leak55"
  }
  provisioner "remote-exec" {
    inline = ["leak56"]
    script = "leak57"
  }
}

resource "aws_db_instance" "standby" {
  engine = "keep25"
  /* note */ master_password {
    value = "leak60"
  }
  password = "a" + /* x */
    "leak61"
  identifier = "keep26"
}

resource "aws_ecs_task_definition" "api_token" {
  family = "leak79"
  container_definitions = <<EOF
[{"name": "app", "environment": [{"name": "DB_PASS", "value": "leak62"}]}]
EOF
}

module "vault_secret" {
  ttl = 3600123
}

variable "db_password_length" {
  default = 4242424
}

# Patterns that a narrower rule exposed: under a secret-like label every value is hidden;
# the same attributes under an ordinary label stay visible.
resource "docker_container" "api_token" {
  env  = ["DB_PASS=leak80"]
  args = ["leak81"]
}

resource "aws_cloudformation_stack" "secrets" {
  parameters = {
    DBPass = "leak82"
  }
}

resource "azurerm_linux_web_app" "secrets" {
  app_settings = {
    DB = "leak83"
  }
}

resource "random_id" "secret" {
  keepers = {
    k = "leak84"
  }
}

resource "docker_container" "web" {
  image = "keep30"
  env   = ["MODE=keep31"]
  args  = ["keep32"]
}

resource "aws_cloudformation_stack" "network" {
  parameters = {
    Size = "keep33"
  }
}

resource "random_id" "suffix" {
  keepers = {
    k = "keep34"
  }
}
