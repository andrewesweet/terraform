// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: BUSL-1.1

package graph

import (
	"testing"

	"github.com/hashicorp/terraform/internal/addrs"
	"github.com/hashicorp/terraform/internal/providers"
	testing_provider "github.com/hashicorp/terraform/internal/providers/testing"
)

// exitingProvider is a provider whose plugin process can be marked as exited.
type exitingProvider struct {
	*testing_provider.MockProvider
	exited bool
}

func (p *exitingProvider) Exited() bool {
	return p.exited
}

func TestMockedProviders(t *testing.T) {
	addr := addrs.NewDefaultProvider("test")

	var launched []*exitingProvider
	factory := func() (providers.Interface, error) {
		p := &exitingProvider{MockProvider: &testing_provider.MockProvider{}}
		launched = append(launched, p)
		return p, nil
	}

	mocked := &MockedProviders{}

	first, err := mocked.get(addr, factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := mocked.get(addr, factory)
	if err != nil {
		t.Fatal(err)
	}
	if len(launched) != 1 {
		t.Fatalf("expected 1 launch for two files but got %d", len(launched))
	}
	if launched[0].CloseCalled {
		t.Fatal("a file closing the provider closed the shared instance")
	}

	// A later file gets a new instance if the shared plugin has exited.
	second.Close()
	launched[0].exited = true
	if _, err := mocked.get(addr, factory); err != nil {
		t.Fatal(err)
	}
	if len(launched) != 2 {
		t.Fatalf("expected a new launch after the plugin exited but got %d launches", len(launched))
	}
	if !launched[0].CloseCalled {
		t.Fatal("the exited instance was not closed")
	}

	if err := mocked.Close(); err != nil {
		t.Fatal(err)
	}
	if !launched[1].CloseCalled {
		t.Fatal("closing the shared providers did not close the instance")
	}
}
