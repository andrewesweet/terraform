run "assertion_fails" {
  command = plan

  assert {
    condition     = output.legacy.name == "b"
    error_message = "unexpected name"
  }
}
