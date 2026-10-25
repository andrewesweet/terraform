variable "trigger" { type = string }
resource "terraform_data" "example" {
  triggers_replace = var.trigger
  provisioner "local-exec" {
    command = "echo once >> provisioners.txt"
  }
}
