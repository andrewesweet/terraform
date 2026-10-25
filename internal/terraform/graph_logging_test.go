// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package terraform

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/logging"
)

// Exercise process-initialized logging rather than replacing the standard
// logger's writer, so that level filtering, JSON and sinks all use real wiring.
func TestGraphLogging(t *testing.T) {
	for _, tc := range []struct {
		name, level, core, graphTrace string
		debug, trace, sink            bool
	}{
		{name: "default"},
		{name: "off", level: "off"},
		{name: "info", level: "info"},
		{name: "debug", level: "debug", debug: true},
		{name: "trace", level: "trace", debug: true, trace: true},
		{name: "graph trace", level: "trace", graphTrace: "1", debug: true, trace: true},
		{name: "graph trace without logging", graphTrace: "1"},
		{name: "json", level: "JSON", debug: true, trace: true},
		{name: "core fallback", core: "trace", debug: true, trace: true},
		{name: "invalid fallback", level: "invalid", debug: true, trace: true},
		{name: "sink without logging", debug: true, trace: true, sink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "terraform.log")
			// Clear inherited logging settings before the child's init functions.
			env := []string{}
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "TF_LOG") && !strings.HasPrefix(entry, "TF_GRAPH_") {
					env = append(env, entry)
				}
			}
			env = append(env, "TF_GRAPH_LOG_TEST_CHILD=1", "TF_LOG="+tc.level,
				"TF_LOG_CORE="+tc.core, "TF_GRAPH_TRACE="+tc.graphTrace)
			if tc.sink {
				env = append(env, "TF_GRAPH_LOG_TEST_SINK="+logPath)
			} else {
				env = append(env, "TF_LOG_PATH="+logPath)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestGraphLoggingChild$")
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("child: %s\n%s", err, out)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}

			var got, want []string
			if tc.level == "JSON" {
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var entry struct {
						Message string `json:"@message"`
					}
					if err := json.Unmarshal([]byte(line), &entry); err != nil {
						t.Fatal(err)
					}
					got = append(got, entry.Message)
				}
			} else {
				prefix := regexp.MustCompile(`(?m)^\S+ \[(?:TRACE|DEBUG)\] `)
				for _, msg := range prefix.Split(string(data), -1) {
					if msg = strings.TrimSpace(msg); msg != "" {
						got = append(got, msg)
					}
				}
			}
			if tc.trace {
				want = append(want,
					"Executing graph transform *terraform.graphTransformerMulti",
					"(graphTransformerMulti) Executing graph transform *terraform.testBasicGraphBuilderTransform",
					"(graphTransformerMulti) Completed graph transform *terraform.testBasicGraphBuilderTransform with new graph:\n  1 - terraform.testV\n  ------",
					"(graphTransformerMulti) Executing graph transform *terraform.testBasicGraphBuilderTransform",
					"(graphTransformerMulti) Completed graph transform *terraform.testBasicGraphBuilderTransform (no changes)")
				if tc.graphTrace != "" {
					want = append(want, "Completed graph transform *terraform.graphTransformerMulti with new graph:\n  1 - terraform.testV\n  ------")
				} else {
					want = append(want, "Completed graph transform:\n1 - terraform.testV\n  ------")
				}
			}
			if tc.debug {
				want = append(want,
					`ReferenceTransformer: "A" references: []`,
					`ReferenceTransformer: "B" references: [A]`,
					`ReferenceTransformer: "B" references: A`)
			}
			// Vertex traversal order is unspecified, but each complete message,
			// including graph indentation and the number of messages, must match.
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wrong logs\ngot:  %q\nwant: %q", got, want)
			}
		})
	}
}

func TestGraphLoggingChild(t *testing.T) {
	if os.Getenv("TF_GRAPH_LOG_TEST_CHILD") == "" {
		t.Skip("only run in the logging subprocess")
	}
	if path := os.Getenv("TF_GRAPH_LOG_TEST_SINK"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		logging.RegisterSink(f)
	}
	b := &BasicGraphBuilder{Steps: []GraphTransformer{GraphTransformMulti(
		&testBasicGraphBuilderTransform{testV(1)},
		&testBasicGraphBuilderTransform{testV(1)},
	)}}
	g, diags := b.Build(addrs.RootModuleInstance)
	if diags.HasErrors() {
		t.Fatal(diags.Err())
	}
	if got := g.String(); got != "1\n" {
		t.Fatalf("wrong built graph: %q", got)
	}

	g = &Graph{Path: addrs.RootModuleInstance}
	g.Add(&graphNodeRefParentTest{NameValue: "A", Names: []string{"A"}})
	g.Add(&graphNodeRefChildTest{NameValue: "B", Refs: []string{"A"}})
	if err := (&ReferenceTransformer{}).Transform(g); err != nil {
		t.Fatal(err)
	}
	if got := g.String(); got != "A\nB\n  A\n" {
		t.Fatalf("wrong reference graph: %q", got)
	}
}
