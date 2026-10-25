// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package command

import (
	"encoding/json"
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
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// Pin the command's rendering as well as its exit status. In particular, a
// validation diagnostic belongs to every run, even when those runs share a
// configuration and cannot proceed to planning.
func TestTest_ValidationPerFile(t *testing.T) {
	for _, fixture := range []string{"repeated", "distinct", "overrides", "invalid", "provisioners"} {
		for _, format := range []string{"human", "json"} {
			t.Run(fixture+"/"+format, func(t *testing.T) {
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
				if fixture == "provisioners" {
					meta.testingOverrides = nil
				}
				init := &InitCommand{Meta: meta}
				if code := init.Run(nil); code != 0 {
					t.Fatalf("init returned %d: %s", code, done(t).All())
				}
				streams, done = terminal.StreamsForTesting(t)
				meta.Streams, meta.View = streams, views.NewView(streams)
				command := &TestCommand{Meta: meta}
				args := []string{"-no-color", "-run-parallelism=1"}
				if format == "json" {
					args = append(args, "-json")
				}
				code := command.Run(args)
				output := done(t)
				wantCode := 0
				if fixture == "invalid" {
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
				golden := filepath.Join(source, format+".golden")
				expected, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if fixture == "repeated" || fixture == "invalid" {
					// Independent parallel runs have no defined completion order.
					// Keep each run's diagnostics ordered, but compare the runs by name.
					actual = validationRunOrder(actual, format)
					expected = []byte(validationRunOrder(string(expected), format))
				}
				if diff := cmp.Diff(string(expected), actual); diff != "" {
					t.Errorf("command output changed (-want +got):\n%s", diff)
				}
				if fixture == "invalid" && validations.Load() != 3 {
					t.Errorf("validation calls = %d, want 3", validations.Load())
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
