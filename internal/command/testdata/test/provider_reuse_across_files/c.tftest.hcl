provider "test" {
  data_prefix = "c"
}

mock_provider "test" {
  alias = "mocked"
}

run "test" {
  assert {
    condition     = data.test_data_source.real.value == "configured-for-c"
    error_message = "file c did not see its own provider configuration"
  }

  assert {
    condition     = !contains(["mocked-in-a", "overridden-in-b"], test_resource.mocked.id)
    error_message = "file c saw mock data or overrides from another file"
  }
}
