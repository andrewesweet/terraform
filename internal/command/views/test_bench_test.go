// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package views

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/command/arguments"
	"github.com/hashicorp/terraform/internal/command/jsonprovider"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/configs/configschema"
	"github.com/hashicorp/terraform/internal/moduletest"
	"github.com/hashicorp/terraform/internal/plans"
	"github.com/hashicorp/terraform/internal/providers"
	"github.com/hashicorp/terraform/internal/states"
	"github.com/hashicorp/terraform/internal/terminal"
	"github.com/hashicorp/terraform/internal/terraform"
)

func BenchmarkTestJSONRunVerbose(b *testing.B) {
	b.Run("included", func(b *testing.B) {
		benchmarkTestJSONRunVerbose(b, false)
	})
	b.Run("omitted", func(b *testing.B) {
		benchmarkTestJSONRunVerbose(b, true)
	})
}

func benchmarkTestJSONRunVerbose(b *testing.B, noProviderSchemas bool) {
	streams := discardStreamsForBench(b)
	view := NewTest(arguments.ViewJSON, NewView(streams), noProviderSchemas)

	run, file := syntheticVerboseRun(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		view.Run(run, file, moduletest.Complete, 0)
	}
	b.StopTimer()

	if !noProviderSchemas {
		schemasJSON, err := json.Marshal(jsonprovider.MarshalForRenderer(&terraform.Schemas{
			Providers: run.Verbose.Providers,
		}))
		if err != nil {
			b.Fatalf("failed to marshal synthetic provider schemas: %s", err)
		}
		b.ReportMetric(float64(len(schemasJSON)), "schema-bytes/msg")
	}
}

func discardStreamsForBench(tb testing.TB) *terminal.Streams {
	tb.Helper()

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		tb.Fatalf("failed to open the null device: %s", err)
	}
	tb.Cleanup(func() { devNull.Close() })

	return &terminal.Streams{
		Stdout: &terminal.OutputStream{File: devNull},
		Stderr: &terminal.OutputStream{File: devNull},
		Stdin:  &terminal.InputStream{File: devNull},
	}
}

func syntheticVerboseRun(tb testing.TB) (*moduletest.Run, *moduletest.File) {
	tb.Helper()

	changeValue := cty.ObjectVal(map[string]cty.Value{
		"value": cty.StringVal("foobar"),
	})
	changeType := cty.Object(map[string]cty.Type{
		"value": cty.String,
	})
	dv, err := plans.NewDynamicValue(changeValue, changeType)
	if err != nil {
		tb.Fatalf("failed to create dynamic value: %s", err)
	}

	run := &moduletest.Run{
		Name:   "run_block",
		Status: moduletest.Pass,
		Config: &configs.TestRun{
			Command: configs.PlanTestCommand,
		},
		Verbose: &moduletest.Verbose{
			Plan: &plans.Plan{
				Changes: &plans.ChangesSrc{
					Resources: []*plans.ResourceInstanceChangeSrc{
						{
							Addr: addrs.AbsResourceInstance{
								Module: addrs.RootModuleInstance,
								Resource: addrs.ResourceInstance{
									Resource: addrs.Resource{
										Mode: addrs.ManagedResourceMode,
										Type: "test_resource",
										Name: "creating",
									},
								},
							},
							PrevRunAddr: addrs.AbsResourceInstance{
								Module: addrs.RootModuleInstance,
								Resource: addrs.ResourceInstance{
									Resource: addrs.Resource{
										Mode: addrs.ManagedResourceMode,
										Type: "test_resource",
										Name: "creating",
									},
								},
							},
							ProviderAddr: addrs.AbsProviderConfig{
								Module: addrs.RootModule,
								Provider: addrs.Provider{
									Hostname:  addrs.DefaultProviderRegistryHost,
									Namespace: "hashicorp",
									Type:      "test",
								},
							},
							ChangeSrc: plans.ChangeSrc{
								Action: plans.Create,
								After:  dv,
							},
						},
					},
				},
			},
			State: states.NewState(),
			Config: &configs.Config{
				Module: &configs.Module{
					ProviderRequirements: &configs.RequiredProviders{},
				},
			},
			Providers: syntheticProviderSchemas(),
		},
	}
	return run, &moduletest.File{Name: "main.tftest.hcl"}
}

func syntheticProviderSchemas() map[addrs.Provider]providers.ProviderSchema {
	const (
		resourceTypes = 250
		dataSources   = 100
		functions     = 40
	)

	provider := addrs.Provider{
		Hostname:  addrs.DefaultProviderRegistryHost,
		Namespace: "hashicorp",
		Type:      "test",
	}

	resourceTypesMap := syntheticSchemaMap("resource", resourceTypes, 12, 2)
	resourceTypesMap["test_resource"] = providers.Schema{
		Body: &configschema.Block{
			Attributes: map[string]*configschema.Attribute{
				"value": {Type: cty.String, Optional: true},
			},
		},
	}

	return map[addrs.Provider]providers.ProviderSchema{
		provider: {
			Provider:      providers.Schema{Body: syntheticBlock("provider", 5, 1)},
			ResourceTypes: resourceTypesMap,
			DataSources:   syntheticSchemaMap("data", dataSources, 8, 1),
			Functions:     syntheticFunctions(functions),
		},
	}
}

func syntheticSchemaMap(kind string, count, attrs, blocks int) map[string]providers.Schema {
	schemas := make(map[string]providers.Schema, count)
	for i := 0; i < count; i++ {
		schemas[fmt.Sprintf("synthetic_%s_%03d", kind, i)] = providers.Schema{
			Body: syntheticBlock(kind, attrs, blocks),
		}
	}
	return schemas
}

func syntheticBlock(kind string, attrs, blocks int) *configschema.Block {
	block := &configschema.Block{
		Attributes:      make(map[string]*configschema.Attribute, attrs),
		BlockTypes:      make(map[string]*configschema.NestedBlock, blocks),
		Description:     fmt.Sprintf("A synthetic %s block standing in for a documented resource type from a large mainstream provider.", kind),
		DescriptionKind: configschema.StringPlain,
	}
	for i := 0; i < attrs; i++ {
		var typ cty.Type
		switch i % 5 {
		case 0:
			typ = cty.String
		case 1:
			typ = cty.Number
		case 2:
			typ = cty.Bool
		case 3:
			typ = cty.List(cty.String)
		default:
			typ = cty.Map(cty.String)
		}
		block.Attributes[fmt.Sprintf("attribute_%02d", i)] = &configschema.Attribute{
			Type:            typ,
			Optional:        i%3 == 0,
			Computed:        i%3 != 0,
			Description:     fmt.Sprintf("Attribute %d of this synthetic block, with a description of realistic length for a mainstream provider schema, explaining how the value behaves.", i),
			DescriptionKind: configschema.StringPlain,
		}
	}
	for i := 0; i < blocks; i++ {
		block.BlockTypes[fmt.Sprintf("nested_block_%02d", i)] = &configschema.NestedBlock{
			Nesting: configschema.NestingList,
			Block:   *syntheticBlock(kind+"_nested", 4, 0),
		}
	}
	return block
}

func syntheticFunctions(count int) map[string]providers.FunctionDecl {
	functions := make(map[string]providers.FunctionDecl, count)
	for i := 0; i < count; i++ {
		functions[fmt.Sprintf("synthetic_function_%02d", i)] = providers.FunctionDecl{
			Parameters: []providers.FunctionParam{
				{Name: "input", Type: cty.String},
			},
			ReturnType: cty.String,
			Summary:    fmt.Sprintf("Synthetic function %d with a summary of realistic length.", i),
		}
	}
	return functions
}
