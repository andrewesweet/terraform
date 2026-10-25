// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package graph

import (
	"errors"
	"fmt"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/moduletest"
	"github.com/hashicorp/terraform/internal/providers"
	"github.com/hashicorp/terraform/internal/terraform"
)

// TestProvidersTransformer is a GraphTransformer that gathers all the providers
// from the module configurations that the test runs depend on and attaches the
// required providers to the test run nodes.
type TestProvidersTransformer struct {
	Config          *configs.Config
	File            *moduletest.File
	Providers       map[addrs.Provider]providers.Factory
	MockedProviders *MockedProviders
}

func (t *TestProvidersTransformer) Transform(g *terraform.Graph) error {

	type tuple struct {
		configure *NodeProviderConfigure
		close     *NodeProviderClose
	}

	nodes := make(map[string]map[string]tuple)

	for _, config := range t.File.Config.Providers {
		provider := t.Config.ProviderForConfigAddr(config.Addr())

		factory, ok := t.Providers[provider]
		if !ok {
			return fmt.Errorf("unknown provider %s", provider)
		}

		var impl providers.Interface
		var err error
		if config.Mock {
			impl, err = t.MockedProviders.get(provider, factory)
		} else {
			impl, err = factory()
		}
		if err != nil {
			return fmt.Errorf("could not create provider instance: %w", err)
		}

		if config.Mock {
			impl = &providers.Mock{
				Provider: impl,
				Data:     config.MockData,
			}
		}

		addr := addrs.RootProviderConfig{
			Provider: provider,
			Alias:    config.Alias,
		}

		configure := &NodeProviderConfigure{
			name:     config.Name,
			alias:    config.Alias,
			Addr:     addr,
			File:     t.File,
			Config:   config,
			Provider: impl,
			Schema:   impl.GetProviderSchema(),
		}
		g.Add(configure)

		close := &NodeProviderClose{
			name:     config.Name,
			alias:    config.Alias,
			Addr:     addr,
			File:     t.File,
			Config:   config,
			Provider: impl,
		}
		g.Add(close)

		if _, exists := nodes[config.Name]; !exists {
			nodes[config.Name] = make(map[string]tuple)
		}
		nodes[config.Name][config.Alias] = tuple{
			configure: configure,
			close:     close,
		}

		// make sure the provider is only closed after the provider starts.
		g.Connect(close, configure)
	}

	for vertex := range g.VerticesSeq() {
		if vertex, ok := vertex.(*NodeTestRun); ok {
			// providers aren't referenceable so the automatic reference
			// transformer won't do this.

			if len(vertex.TestRun().Config.Providers) > 0 {
				for _, ref := range vertex.TestRun().Config.Providers {
					if node, ok := nodes[ref.InParent.Name][ref.InParent.Alias]; ok {
						g.Connect(vertex, node.configure)
					}
				}
			} else {
				for provider := range requiredProviders(vertex.run.ModuleConfig) {
					name := t.Config.Module.LocalNameForProvider(provider.Provider)
					if node, ok := nodes[name][provider.Alias]; ok {
						g.Connect(vertex, node.configure)
					}
				}
			}
		}

		if vertex, ok := vertex.(*TeardownSubgraph); ok {
			for _, node := range nodes {
				for _, node := range node {
					// close all the providers after the states have been
					// cleaned up.
					g.Connect(node.close, vertex)
				}
			}
		}
	}
	return nil
}

// MockedProviders holds the provider instances behind mock_provider blocks,
// so that every test file in a run can share them.
//
// A mock never configures the provider behind it (see
// providers.Mock.ConfigureProvider). It only uses that provider for calls that
// need no configuration, such as reading the schema, validating configuration
// and calling provider-defined functions, while each file's mock data and
// overrides stay in that file's own providers.Mock. Sharing the instance
// therefore carries no configuration or mock data from one file to the next,
// and each provider is launched once per run instead of once per test file.
type MockedProviders struct {
	instances map[addrs.Provider]providers.Interface
}

// get returns the shared instance of the given provider. It launches the
// instance with factory if no instance has been launched yet, or if the
// previous instance's plugin process has exited.
func (m *MockedProviders) get(addr addrs.Provider, factory providers.Factory) (providers.Interface, error) {
	if impl, ok := m.instances[addr]; ok {
		if p, ok := impl.(interface{ Exited() bool }); !ok || !p.Exited() {
			return sharedProvider{impl}, nil
		}
		// The plugin crashed during an earlier file. Later files start a
		// new instance, as they would if each file launched its own.
		impl.Close()
	}

	impl, err := factory()
	if err != nil {
		return nil, err
	}
	if m.instances == nil {
		m.instances = make(map[addrs.Provider]providers.Interface)
	}
	m.instances[addr] = impl
	return sharedProvider{impl}, nil
}

// Close closes every shared provider instance. Call it once all test files
// have finished.
func (m *MockedProviders) Close() error {
	var errs []error
	for _, impl := range m.instances {
		errs = append(errs, impl.Close())
	}
	m.instances = nil
	return errors.Join(errs...)
}

// sharedProvider keeps a test file from closing a shared provider instance
// when the file finishes.
type sharedProvider struct {
	providers.Interface
}

func (sharedProvider) Close() error {
	return nil
}
