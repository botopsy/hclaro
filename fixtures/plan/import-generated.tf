# __generated__ by Terraform
# Please review these resources and move them into your main configuration files.

# __generated__ by Terraform from "hclaro-legacy-logs"
resource "aws_s3_bucket" "legacy_logs" {
  bucket        = "hclaro-legacy-logs"
  force_destroy = false
  tags = {
    env = "demo"
  }
  replication_token = "DO_NOT_LEAK_HCLARO_TEST_SECRET"
  policy = <<EOT
{"Statement":[{"Sid":"DO_NOT_LEAK_HCLARO_TEST_SECRET"}]}
EOT
}
