// SPDX-FileCopyrightText: 2022 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package rib

import (
	"net"
	"net/netip"

	"github.com/osrg/gobgp/v3/pkg/packet/bgp"
)

// PeerKey is the key used to identify a peer
type PeerKey = netip.Addr

// PeerInfo contains some information attached to a peer.
type PeerInfo struct {
	reference uint32 // used as a reference in the RIB
}

func (p *Provider) AddPeer(pkey PeerKey) *PeerInfo {
	p.lastPeerReference++
	if p.lastPeerReference == 0 {
		// This is a very unlikely event, but we don't
		// have anything better. Let's crash (and
		// hopefully be restarted).
		p.r.Fatal().Msg("too many peer up events")
		go p.Stop()
	}
	pinfo := &PeerInfo{
		reference: p.lastPeerReference,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pinfo, has := p.peers[pkey]; has {
		return pinfo
	}
	p.metrics.peers.WithLabelValues(pkey.String()).Inc()
	p.peers[pkey] = pinfo
	return pinfo
}

// RemovePeer remove a peer (with lock held)
func (p *Provider) RemovePeer(pkey PeerKey, reason string) {
	exporterStr := pkey.Unmap().String()
	p.r.Info().Msgf("remove peer %s for exporter %s (reason: %s)", exporterStr, exporterStr, reason)
	select {
	case p.peerRemovalChan <- pkey:
		return
	default:
	}
	p.metrics.peerRemovalQueueFull.WithLabelValues(exporterStr).Inc()
	p.mu.Unlock()
	select {
	case p.peerRemovalChan <- pkey:
	}
	p.mu.Lock()
}

func (p *Provider) AddRoute(
	pkey PeerKey,
	afi uint16,
	safi uint8,
	rd RD,
	pathID uint32,
	prefix netip.Addr,
	plen int,
	rta RouteAttributes,
	nh netip.Addr,
) (cnt int) {
	defer func() {
		p.metrics.routes.WithLabelValues(pkey.String()).Add(float64(cnt))
	}()

	if !p.isAcceptedRD(rd) {
		p.metrics.ignored.WithLabelValues(pkey.String(), "rd", "").Inc()
		return 0
	}
	p.mu.RLock()
	pinfo, ok := p.peers[pkey]
	p.mu.RUnlock()
	if !ok {
		exporterStr := pkey.Unmap().String()
		// We may have missed the peer down notification?
		p.r.Info().Msgf("received route monitoring from exporter %s for peer %s, but no peer up",
			exporterStr, exporterStr)
		pinfo = p.AddPeer(pkey)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rib.addPrefix(prefix, plen, route{
		peer: pinfo.reference,
		nlri: p.rib.nlris.Put(nlri{
			family: bgp.AfiSafiToRouteFamily(afi, safi),
			path:   pathID,
			rd:     rd,
		}),
		nextHop:    p.rib.nextHops.Put(nextHop(nh)),
		attributes: p.rib.rtas.Put(rta),
	})
}

func (p *Provider) RemoveRoute(
	pkey PeerKey,
	afi uint16,
	safi uint8,
	rd RD,
	pathID uint32,
	prefix net.IP,
	plen int,
) (cnt int) {
	defer func() {
		p.metrics.routes.WithLabelValues(pkey.String()).Sub(float64(cnt))
	}()
	p.mu.RLock()
	pinfo, ok := p.peers[pkey]
	p.mu.RUnlock()
	if !ok {
		return 0
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	pf, _ := netip.AddrFromSlice(prefix)
	if nlriRef, ok := p.rib.nlris.Ref(nlri{
		family: bgp.AfiSafiToRouteFamily(afi, safi),
		rd:     rd,
		path:   pathID,
	}); ok {
		return p.rib.removePrefix(pf, plen, route{
			peer: pinfo.reference,
			nlri: nlriRef,
		})
	}

	return 0
}

func (p *Provider) isAcceptedRD(rd RD) bool {
	if len(p.acceptedRDs) == 0 {
		return true
	}
	_, ok := p.acceptedRDs[uint64(rd)]
	return ok
}
