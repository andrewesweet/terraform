// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	testing_command "github.com/hashicorp/terraform/internal/command/testing"
	"github.com/hashicorp/terraform/internal/command/views"
	"github.com/hashicorp/terraform/internal/providers"
	"github.com/hashicorp/terraform/internal/terminal"
	"github.com/hashicorp/terraform/internal/terraform"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// Pin the command's rendering as well as its exit status. In particular, a
// validation diagnostic belongs to every run, even when those runs share a
// configuration and cannot proceed to planning.
func TestTest_ValidationPerFile(t *testing.T) {
	for _, fixture := range []string{"repeated", "distinct", "overrides", "invalid", "invalid-reference", "provisioners"} {
		for _, mode := range []struct {
			format      string
			parallelism int
		}{{"human", 1}, {"json", 1}, {"human", 8}, {"json", 8}} {
			format := mode.format
			t.Run(fmt.Sprintf("%s/%s/%d", fixture, format, mode.parallelism), func(t *testing.T) {
				source, err := filepath.Abs(testFixturePath(filepath.Join("test", "validation_per_file", fixture)))
				if err != nil {
					t.Fatal(err)
				}
				td := t.TempDir()
				testCopyDir(t, source, td)
				t.Chdir(td)
				provider := testing_command.NewProvider(nil)
				var validations atomic.Int32
				provider.Provider.ValidateResourceConfigFn = func(req providers.ValidateResourceConfigRequest) providers.ValidateResourceConfigResponse {
					validations.Add(1)
					severity := tfdiags.Warning
					if fixture == "invalid" {
						severity = tfdiags.Error
					}
					return providers.ValidateResourceConfigResponse{Diagnostics: tfdiags.Diagnostics{
						tfdiags.Sourceless(severity, "First validation diagnostic", "The resource configuration was checked."),
						tfdiags.Sourceless(severity, "Second validation diagnostic", "Diagnostic order must be preserved."),
					}}
				}
				streams, done := terminal.StreamsForTesting(t)
				meta := Meta{testingOverrides: metaOverridesForProvider(provider.Provider), Ui: testUiWrapped(t), View: views.NewView(streams), Streams: streams,
					ProviderSource: newMockProviderSource(t, map[string][]string{"test": {"1.0.0"}})}
				if fixture == "provisioners" || fixture == "invalid-reference" {
					meta.testingOverrides = nil
				}
				init := &InitCommand{Meta: meta}
				if code := init.Run(nil); code != 0 {
					t.Fatalf("init returned %d: %s", code, done(t).All())
				}
				if fixture == "provisioners" {
					checkValidationConfigurationUnchanged(t, &meta)
				}
				streams, done = terminal.StreamsForTesting(t)
				meta.Streams, meta.View = streams, views.NewView(streams)
				command := &TestCommand{Meta: meta}
				args := []string{"-no-color", "-junit-xml=report.xml", fmt.Sprintf("-run-parallelism=%d", mode.parallelism)}
				if format == "json" {
					args = append(args, "-json")
				}
				code := command.Run(args)
				output := done(t)
				wantCode := 0
				if strings.HasPrefix(fixture, "invalid") {
					wantCode = 1
				}
				if code != wantCode {
					t.Fatalf("test returned %d, want %d: %s", code, wantCode, output.All())
				}
				actual := output.Stdout() + "\n--- stderr ---\n" + output.Stderr()
				if format == "json" {
					var messages []string
					for _, line := range strings.Split(output.Stdout(), "\n") {
						if line == "" {
							continue
						}
						var message map[string]interface{}
						if err := json.Unmarshal([]byte(line), &message); err != nil {
							t.Fatal(err)
						}
						if message["type"] == "version" {
							continue
						}
						delete(message, "@timestamp")
						if run, ok := message["test_run"].(map[string]interface{}); ok {
							delete(run, "elapsed")
						}
						encoded, err := json.Marshal(message)
						if err != nil {
							t.Fatal(err)
						}
						messages = append(messages, string(encoded))
					}
					actual = strings.Join(messages, "\n") + "\n--- stderr ---\n" + output.Stderr()
				}
				golden := filepath.Join(source, format+".expected")
				expected, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if mode.parallelism > 1 {
					actual = validationOutputMultiset(actual)
					expected = []byte(validationOutputMultiset(string(expected)))
				} else if fixture == "repeated" || strings.HasPrefix(fixture, "invalid") {
					// Independent parallel runs have no defined completion order.
					// Keep each run's diagnostics ordered, but compare the runs by name.
					actual = validationRunOrder(actual, format)
					expected = []byte(validationRunOrder(string(expected), format))
				}
				if diff := cmp.Diff(string(expected), actual); diff != "" {
					t.Errorf("command output changed (-want +got):\n%s", diff)
				}
				if fixture == "invalid" {
					if validations.Load() != 1 {
						t.Errorf("validation calls = %d, want 1", validations.Load())
					}
				}
				if strings.HasPrefix(fixture, "invalid") {
					checkValidationRunErrors(t, fixture)
				}
				// Four plans plus validation of three distinct (configuration,
				// provider map) pairs. The second and fourth runs share a pair.
				if fixture == "distinct" && validations.Load() != 7 {
					t.Errorf("validation calls = %d, want 7", validations.Load())
				}
				if fixture == "provisioners" {
					executions, err := os.ReadFile("provisioners.txt")
					if err != nil {
						t.Fatal(err)
					}
					if string(executions) != "once\nonce\nonce\n" {
						t.Errorf("provisioner blocks accumulated across runs: %q", executions)
					}
				}
			})
		}
	}
}

// Only the independent-run region is unordered; retain file events and the
// order of diagnostics within each run exactly as emitted by the command.
func validationRunOrder(output, format string) string {
	lines := strings.Split(output, "\n")
	start, end := -1, -1
	groups := make(map[string][]string)
	var name string
	for i, line := range lines {
		if format == "json" {
			var message map[string]interface{}
			if json.Unmarshal([]byte(line), &message) != nil {
				continue
			}
			run, ok := message["@testrun"].(string)
			if !ok {
				if start >= 0 {
					end = i
					break
				}
				continue
			}
			name = run
		} else {
			if strings.HasPrefix(line, "  run ") {
				name = strings.Split(line, "\"")[1]
			}
			if strings.HasPrefix(line, "main.tftest.hcl... tearing down") {
				end = i
				break
			}
			if name == "" {
				continue
			}
		}
		if start < 0 {
			start = i
		}
		groups[name] = append(groups[name], line)
	}
	if start < 0 || end < 0 {
		return output
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	result := append([]string(nil), lines[:start]...)
	for _, name := range names {
		result = append(result, groups[name]...)
	}
	result = append(result, lines[end:]...)
	return strings.Join(result, "\n")
}

func validationOutputMultiset(output string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func checkValidationRunErrors(t *testing.T, fixture string) {
	t.Helper()
	// Human diagnostics have no run label and can interleave. The command's
	// JUnit report exposes the attribution that is not present in that view.
	source, err := os.ReadFile("report.xml")
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Suites []struct {
			Errors int `xml:"errors,attr"`
			Cases  []struct {
				Name  string `xml:"name,attr"`
				Error *struct {
					Body string `xml:",chardata"`
				} `xml:"error"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(source, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Suites) != 1 || len(report.Suites[0].Cases) != 3 || report.Suites[0].Errors != 3 {
		t.Fatalf("expected three errored runs: %s", source)
	}
	diagnostics := []string{"Error: First validation diagnostic", "The resource configuration was checked.", "Error: Second validation diagnostic", "Diagnostic order must be preserved."}
	if fixture == "invalid-reference" {
		diagnostics = []string{"Error: Reference to undeclared input variable", "on main.tf line 2", "input = var.not_real"}
	}
	var names []string
	for _, run := range report.Suites[0].Cases {
		names = append(names, run.Name)
		if run.Error == nil {
			t.Fatalf("run %s must have Error status", run.Name)
		}
		for _, text := range diagnostics {
			if strings.Count(run.Error.Body, text) != 1 {
				t.Errorf("run %s must receive exactly one %q: %s", run.Name, text, run.Error.Body)
			}
		}
	}
	if diff := cmp.Diff([]string{"first", "second", "third"}, names); diff != "" {
		t.Errorf("wrong run attribution: %s", diff)
	}
}

func checkValidationConfigurationUnchanged(t *testing.T, meta *Meta) {
	t.Helper()
	config, diags := meta.loadConfig(".")
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	opts, err := meta.contextOpts()
	if err != nil {
		t.Fatal(err)
	}
	resource := config.Module.ManagedResources["terraform_data.example"]
	connection := resource.Managed.Provisioners[0].Connection.Config
	baseConnection := resource.Managed.Connection.Config
	// Do not go through the memo here: repeated validation of the same loaded
	// configuration must remain safe in its own right (regression for #34009).
	for i := 0; i < 4; i++ {
		ctx, diags := terraform.NewContext(opts)
		if diags.HasErrors() {
			t.Fatal(diags.Err())
		}
		if diags := ctx.Validate(config, nil); diags.HasErrors() {
			t.Fatal(diags.Err())
		}
		if len(resource.Managed.Provisioners) != 1 || resource.Managed.Provisioners[0].Connection.Config != connection || resource.Managed.Connection.Config != baseConnection {
			t.Fatal("validation mutated the shared provisioner/connection configuration")
		}
	}
}
