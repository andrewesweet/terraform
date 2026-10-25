variable "trigger" { type = string }
resource "terraform_data" "example" {
  triggers_replace = var.trigger
  connection {
    host = "example.invalid"
  }
  provisioner "local-exec" {
    connection {
      user = "example"
    }
    command = "echo once >> provisioners.txt"
  }
}
