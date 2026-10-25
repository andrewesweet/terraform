provider "test" {
  data_prefix = "a"
}

mock_provider "test" {
  alias = "mocked"

  mock_resource "test_resource" {
    defaults = {
      id = "mocked-in-a"
    }
  }
}

run "test" {
  assert {
    condition     = data.test_data_source.real.value == "configured-for-a"
    error_message = "file a did not see its own provider configuration"
  }

  assert {
    condition     = test_resource.mocked.id == "mocked-in-a"
    error_message = "file a did not see its own mock data"
  }
}
