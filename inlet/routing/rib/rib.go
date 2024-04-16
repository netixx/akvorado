// SPDX-FileCopyrightText: 2022 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only
package rib

import (
	"context"
	"net/netip"
	"runtime"
	"sync/atomic"
	"unsafe"

	"akvorado/common/helpers/intern"

	"github.com/kentik/patricia"
	tree "github.com/kentik/patricia/generics_tree"
	"github.com/osrg/gobgp/v3/pkg/packet/bgp"
)

// RIB represents the RIB.
type RIB struct {
	tree     *tree.TreeV6[route]
	nlris    *intern.Pool[nlri]
	nextHops *intern.Pool[nextHop]
	rtas     *intern.Pool[RouteAttributes]
}

// route contains the peer (external opaque value), the NLRI, the next
// hop and route attributes. The primary key is prefix (implied), peer
// and nlri.
type route struct {
	peer       uint32
	nlri       intern.Reference[nlri]
	nextHop    intern.Reference[nextHop]
	attributes intern.Reference[RouteAttributes]
}

// nlri is the NLRI for the route (when combined with prefix). The
// route family is included as we may normalize NLRI accross AFI/SAFI.
type nlri struct {
	family bgp.RouteFamily
	path   uint32
	rd     RD
}

// Hash returns a hash for an NLRI
func (n nlri) Hash() uint64 {
	state := rtaHashSeed
	state = rthash((*byte)(unsafe.Pointer(&n.family)), int(unsafe.Sizeof(n.family)), state)
	state = rthash((*byte)(unsafe.Pointer(&n.path)), int(unsafe.Sizeof(n.path)), state)
	state = rthash((*byte)(unsafe.Pointer(&n.rd)), int(unsafe.Sizeof(n.rd)), state)
	return state
}

// Equal tells if two NLRI are equal.
func (n nlri) Equal(n2 nlri) bool {
	return n == n2
}

// nextHop is just an IP address.
type nextHop netip.Addr

// Hash returns a hash for the next hop.
func (nh nextHop) Hash() uint64 {
	ip := netip.Addr(nh).As16()
	state := rtaHashSeed
	return rthash((*byte)(unsafe.Pointer(&ip[0])), 16, state)
}

// Equal tells if two next hops are equal.
func (nh nextHop) Equal(nh2 nextHop) bool {
	return nh == nh2
}

// RouteAttributes is a set of route attributes.
type RouteAttributes struct {
	ASN         uint32
	ASPath      []uint32
	Communities []uint32
	Plen        uint8
	// extendedCommunities []uint64
	LargeCommunities []bgp.LargeCommunity
}

// Hash returns a hash for route attributes. This may seem like black
// magic, but this is important for performance.
func (rta RouteAttributes) Hash() uint64 {
	state := rtaHashSeed
	state = rthash((*byte)(unsafe.Pointer(&rta.ASN)), int(unsafe.Sizeof(rta.ASN)), state)
	if len(rta.ASPath) > 0 {
		state = rthash((*byte)(unsafe.Pointer(&rta.ASPath[0])), len(rta.ASPath)*int(unsafe.Sizeof(rta.ASPath[0])), state)
	}
	if len(rta.Communities) > 0 {
		state = rthash((*byte)(unsafe.Pointer(&rta.Communities[0])), len(rta.Communities)*int(unsafe.Sizeof(rta.Communities[0])), state)
	}
	if len(rta.LargeCommunities) > 0 {
		// There is a test to check that this computation is
		// correct (the struct is 12-byte aligned, not
		// 16-byte).
		state = rthash((*byte)(unsafe.Pointer(&rta.LargeCommunities[0])), len(rta.LargeCommunities)*int(unsafe.Sizeof(rta.LargeCommunities[0])), state)
	}
	return state & rtaHashMask
}

// Equal tells if two route attributes are equal.
func (rta RouteAttributes) Equal(orta RouteAttributes) bool {
	if rta.ASN != orta.ASN {
		return false
	}
	if len(rta.ASPath) != len(orta.ASPath) {
		return false
	}
	if len(rta.Communities) != len(orta.Communities) {
		return false
	}
	if len(rta.LargeCommunities) != len(orta.LargeCommunities) {
		return false
	}
	for idx := range rta.ASPath {
		if rta.ASPath[idx] != orta.ASPath[idx] {
			return false
		}
	}
	for idx := range rta.Communities {
		if rta.Communities[idx] != orta.Communities[idx] {
			return false
		}
	}
	for idx := range rta.LargeCommunities {
		if rta.LargeCommunities[idx] != orta.LargeCommunities[idx] {
			return false
		}
	}
	return true
}

// addPrefix add a new route to the RIB. It returns the number of routes really added.
func (r *RIB) addPrefix(ip netip.Addr, bits int, new route) int {
	v6 := patricia.NewIPv6Address(ip.AsSlice(), uint(bits))
	added, _ := r.tree.AddOrUpdate(v6, new,
		func(r1, r2 route) bool {
			return r1.peer == r2.peer && r1.nlri == r2.nlri
		}, func(old route) route {
			r.nlris.Take(old.nlri)
			r.nextHops.Take(old.nextHop)
			r.rtas.Take(old.attributes)
			return new
		})
	if !added {
		return 0
	}
	return 1
}

// removePrefix removes a route from the RIB. It returns the number of routes really removed.
func (r *RIB) removePrefix(ip netip.Addr, bits int, old route) int {
	v6 := patricia.NewIPv6Address(ip.AsSlice(), uint(bits))
	removed := r.tree.Delete(v6, func(r1, r2 route) bool {
		// This is not enforced/documented, but the route in the tree is the first one.
		if r1.peer == r2.peer && r1.nlri == r2.nlri {
			r.nlris.Take(r1.nlri)
			r.nextHops.Take(r1.nextHop)
			r.rtas.Take(r1.attributes)
			return true
		}
		return false
	}, old)
	return removed
}

// flushPeer removes a whole peer from the RIB, returning the number
// of removed routes.
func (r *RIB) flushPeer(peer uint32) int {
	removed, _ := r.flushPeerContext(nil, peer, 0)
	return removed
}

// flushPeerContext removes a whole peer from the RIB, with a context returning
// the number of removed routes and a bool to say if the operation was completed
// before cancellation.
func (r *RIB) flushPeerContext(ctx context.Context, peer uint32, steps int) (int, bool) {
	done := atomic.Bool{}
	stop := make(chan struct{})
	lastStep := 0
	if ctx != nil {
		defer close(stop)
		go func() {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				done.Store(true)
			}
		}()
	}

	// Flush routes
	removed := 0
	buf := make([]route, 0)
	iter := r.tree.Iterate()
	runtime.Gosched()
	for iter.Next() {
		removed += iter.DeleteWithBuffer(buf, func(payload route, _ route) bool {
			if payload.peer == peer {
				r.nlris.Take(payload.nlri)
				r.nextHops.Take(payload.nextHop)
				r.rtas.Take(payload.attributes)
				return true
			}
			return false
		}, route{})
		if ctx != nil && removed/steps > lastStep {
			runtime.Gosched()
			lastStep = removed / steps
			if done.Load() {
				return removed, false
			}
		}
	}
	return removed, true
}

// newRIB initializes a new RIB.
func newRIB() *RIB {
	return &RIB{
		tree:     tree.NewTreeV6[route](),
		nlris:    intern.NewPool[nlri](),
		nextHops: intern.NewPool[nextHop](),
		rtas:     intern.NewPool[RouteAttributes](),
	}
}
