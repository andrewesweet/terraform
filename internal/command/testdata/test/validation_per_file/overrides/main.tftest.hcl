mock_provider "test" {
  mock_resource "test_resource" {
    defaults = { id = "file-default" }
  }
}
run "first" {
  assert {
    condition = test_resource.example.id == "file-default"
    error_message = "File mocks must be preserved."
  }
}
run "second" {
  state_key = "second"
  override_resource {
    target = test_resource.example
    values = { id = "run-override" }
  }
  assert {
    condition = test_resource.example.id == "run-override"
    error_message = "Run overrides must be preserved."
  }
}
run "third" {
  state_key = "third"
  assert {
    condition = test_resource.example.id == "file-default"
    error_message = "Run overrides must not leak."
  }
}
