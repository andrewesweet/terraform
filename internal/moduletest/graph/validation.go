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

	result.once.Do(func() { result.diags = validate() })
	return result.diags
}
