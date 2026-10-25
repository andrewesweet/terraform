run "first" {
  command = plan
  state_key = "first"
  parallel = true
}

run "second" {
  command = plan
  state_key = "second"
  parallel = true
}

run "third" {
  command = plan
  state_key = "third"
  parallel = true
}
