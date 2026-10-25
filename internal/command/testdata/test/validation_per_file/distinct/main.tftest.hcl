mock_provider "test" {}
mock_provider "test" {
  alias = "other"
}
run "first" { command = plan }
run "second" {
  command = plan
  providers = { test = test.other }
}
run "third" {
  command = plan
  module { source = "./alternate" }
}
run "fourth" {
  command = plan
  providers = { test = test.other }
}
