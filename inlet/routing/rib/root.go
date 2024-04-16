package rib

import (
	"sync"

	"akvorado/common/reporter"

	"github.com/benbjohnson/clock"
)

// Dependencies define the dependencies of the RIB component.
type Dependencies struct {
	Clock clock.Clock
}

type Provider struct {
	r           *reporter.Reporter
	metrics     metrics
	d           *Dependencies
	config      Configuration
	acceptedRDs map[uint64]struct{}

	// RIB management with peers
	rib               *RIB
	peers             map[PeerKey]*PeerInfo
	peerRemovalChan   chan PeerKey
	lastPeerReference uint32
	mu                sync.RWMutex
}

// New creates a new RIB component from its configuration.
func (configuration Configuration) New(r *reporter.Reporter, dependencies Dependencies) (*Provider, error) {
	if dependencies.Clock == nil {
		dependencies.Clock = clock.New()
	}
	p := Provider{
		r:      r,
		d:      &dependencies,
		config: configuration,

		rib:             newRIB(),
		peers:           make(map[PeerKey]*PeerInfo),
		peerRemovalChan: make(chan PeerKey, configuration.RIBPeerRemovalMaxQueue),
	}
	if len(p.config.RDs) > 0 {
		p.acceptedRDs = make(map[uint64]struct{})
		for _, rd := range p.config.RDs {
			p.acceptedRDs[uint64(rd)] = struct{}{}
		}
	}

	p.initMetrics()
	return &p, nil
}

// Start starts the RIB provider.
func (p *Provider) Start() error {
	p.r.Info().Msg("starting RIB provider")
	return nil
}

// Stop stops the RIB provider.
func (p *Provider) Stop() error {
	defer func() {
		close(p.peerRemovalChan)
		p.r.Info().Msg("RIB component stopped")
	}()
	return nil
}
