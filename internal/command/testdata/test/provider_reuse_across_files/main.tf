terraform {
  required_providers {
    test = {
      source = "hashicorp/test"
    }
  }
}

provider "test" {}

provider "test" {
  alias = "mocked"
}

data "test_data_source" "real" {
  id = "shared"
}

resource "test_resource" "mocked" {
  provider = test.mocked
}
