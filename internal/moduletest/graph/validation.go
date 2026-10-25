// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package graph

import (
	"maps"
	"sync"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/configs"
	"github.com/hashicorp/terraform/internal/providers"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// validationCache identifies configurations by their loaded object, not their
// source address: separate module calls can have different configurations. The
// provider map identifies both the destination addresses and the actual provider
// instances, so remapping an alias cannot reuse another provider's diagnostics.
// Test providers are pointer-backed instances held by the file's EvalContext.
// The maps returned by getProviders are fresh per run; their map allocation is
// not part of the identity.
type validationCache struct {
	mu      sync.Mutex
	configs map[*configs.Config][]*validationResult
}

type validationResult struct {
	providers map[addrs.RootProviderConfig]providers.Interface
	once      sync.Once
	diags     tfdiags.Diagnostics
}

func (c *validationCache) validate(config *configs.Config, providers map[addrs.RootProviderConfig]providers.Interface, validate func() tfdiags.Diagnostics) tfdiags.Diagnostics {
	c.mu.Lock()
	if c.configs == nil {
		c.configs = make(map[*configs.Config][]*validationResult)
	}
	var result *validationResult
	for _, candidate := range c.configs[config] {
		if maps.Equal(candidate.providers, providers) {
			result = candidate
			break
		}
	}
	if result == nil {
		result = &validationResult{providers: maps.Clone(providers)}
		c.configs[config] = append(c.configs[config], result)
	}
	c.mu.Unlock()

	// Do not hold the file-wide lock while validating: unrelated configurations
	// may validate concurrently. Once also publishes the diagnostics to waiters.
	result.once.Do(func() { result.diags = validate() })
	return result.diags
}
