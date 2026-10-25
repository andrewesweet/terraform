provider "test" {
  data_prefix = "b"
}

mock_provider "test" {
  alias = "mocked"
}

override_resource {
  target = test_resource.mocked
  values = {
    id = "overridden-in-b"
  }
}

run "test" {
  assert {
    condition     = data.test_data_source.real.value == "configured-for-b"
    error_message = "file b did not see its own provider configuration"
  }

  assert {
    condition     = test_resource.mocked.id == "overridden-in-b"
    error_message = "file b did not see its own override"
  }
}
