// SPDX-FileCopyrightText: 2022 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package biorisobserve

import (
	"context"
	"errors"
	"net/netip"

	"akvorado/inlet/routing/provider"
)

// LookupResult is the result of the Lookup() function.
type LookupResult = provider.LookupResult

var errNoRouteFound = errors.New("no route found")

// Lookup lookups a route for the provided IP address. It favors the
// provided next hop if provided. This is somewhat approximate because
// we use the best route we have, while the exporter may not have this
// best route available. The returned result should not be modified!
// The last parameter, the agent, is ignored by this provider.
func (p *Provider) Lookup(ctx context.Context, ip netip.Addr, nh netip.Addr, exporterAddress netip.Addr) (LookupResult, error) {
	if !p.active.Load() {
		return LookupResult{}, nil
	}
	p.submitObserveRib(observeRIBRequest{
		exporterAddress: exporterAddress,
		ipv6: !ip.Unmap().Is4(),
	})

	// TODO: expire peer when no lookup has been performed for a while
	return p.rib.Lookup(ctx, ip, nh, exporterAddress)
}
