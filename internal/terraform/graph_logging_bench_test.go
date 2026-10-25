// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package terraform

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/dag"
	"github.com/hashicorp/terraform/internal/logging"
)

// Run with TF_LOG and TF_LOG_CORE unset. These benchmarks include the graph
// work that remains after log-only argument construction is removed.
func BenchmarkGraphLoggingDisabled(b *testing.B) {
	if logging.HCLogger().IsDebug() {
		b.Skip("requires debug logging to be disabled")
	}
	vertices := make([]dag.Vertex, 1000)
	root := addrs.Resource{Mode: addrs.ManagedResourceMode, Type: "terraform_data", Name: "root"}.Instance(addrs.NoKey)
	for i := range vertices {
		addr := addrs.Resource{Mode: addrs.ManagedResourceMode, Type: "terraform_data", Name: fmt.Sprintf("item_%04d", i)}.Instance(addrs.NoKey)
		var refs []*addrs.Reference
		if i == 0 {
			addr = root
		} else {
			refs = []*addrs.Reference{{Subject: root}}
		}
		vertices[i] = &graphNodeFakeResourceInstance{Addr: addr.Absolute(addrs.RootModuleInstance), Refs: refs}
	}
	populate := &benchmarkLoggingGraphTransform{vertices: vertices}
	b.Run("Multi", func(b *testing.B) {
		g := &Graph{}
		populate.Transform(g)
		transform := GraphTransformMulti(populate, populate, populate)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := transform.Transform(g); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Build", func(b *testing.B) {
		builder := &BasicGraphBuilder{Steps: []GraphTransformer{populate}}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, diags := builder.Build(addrs.RootModuleInstance); diags.HasErrors() {
				b.Fatal(diags.Err())
			}
		}
	})
	b.Run("References", func(b *testing.B) {
		g := &Graph{}
		populate.Transform(g)
		transform := &ReferenceTransformer{}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := transform.Transform(g); err != nil {
				b.Fatal(err)
			}
		}
	})
}

type benchmarkLoggingGraphTransform struct {
	vertices []dag.Vertex
}

func (t *benchmarkLoggingGraphTransform) Transform(g *Graph) error {
	for i, v := range t.vertices {
		g.Add(v)
		if i != 0 {
			g.Connect(v, t.vertices[0])
		}
	}
	return nil
}
