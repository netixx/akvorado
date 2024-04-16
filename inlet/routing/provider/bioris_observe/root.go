// SPDX-FileCopyrightText: 2024 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package biorisobserve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/bio-routing/bio-rd/cmd/ris/api"
	"github.com/bio-routing/bio-rd/route/api"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/osrg/gobgp/v3/pkg/packet/bgp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/tomb.v2"

	"akvorado/common/reporter"
	"akvorado/inlet/routing/provider"
	"akvorado/inlet/routing/rib"
)

var (
	errNoRouter       = errors.New("no router")
	errNoInstance     = errors.New("no RIS instance available")
	errResultEmpty    = errors.New("result empty")
	errNoPathFound    = errors.New("no path found")
	errInvalidNextHop = errors.New("invalid next hop")
)

// RISInstanceRuntime represents all connections to a single RIS
type RISInstanceRuntime struct {
	conn   *grpc.ClientConn
	client pb.RoutingInformationServiceClient
	config RISInstance
}

// Provider represents the BioRIS routing provider.
type Provider struct {
	r      *reporter.Reporter
	d      *Dependencies
	t      tomb.Tomb
	config Configuration

	metrics              metrics
	clientMetrics        *grpc_prometheus.ClientMetrics
	instances            map[string]*RISInstanceRuntime
	routers              map[netip.Addr][]*RISInstanceRuntime
	active               atomic.Bool
	observeSubscriptions map[string]pb.RoutingInformationService_ObserveRIBClient

	rib *rib.Provider
	mu  sync.RWMutex
}

// Dependencies define the dependencies of the BioRIS Provider.
type Dependencies = provider.Dependencies

// New creates a new BioRIS with ObserveRIB provider.
func (configuration Configuration) New(r *reporter.Reporter, dependencies Dependencies) (provider.Provider, error) {
	ribComponent, err := configuration.RIB.New(
		r,
		rib.Dependencies{
			Clock:  dependencies.Clock,
			Daemon: dependencies.Daemon,
		},
	)
	if err != nil {
		return nil, err
	}
	p := Provider{
		r:                    r,
		d:                    &dependencies,
		config:               configuration,
		instances:            make(map[string]*RISInstanceRuntime),
		routers:              make(map[netip.Addr][]*RISInstanceRuntime),
		rib:                  ribComponent,
		observeSubscriptions: map[string]pb.RoutingInformationService_ObserveRIBClient{},
	}
	p.clientMetrics = grpc_prometheus.NewClientMetrics()
	p.initMetrics()

	return &p, nil
}

// Start starts the bioris provider.
func (p *Provider) Start() error {
	p.r.Info().Msg("starting BioRIS provider")

	// Connect to RIS backend (done in background)
	for _, config := range p.config.RISInstances {
		instance, err := p.Dial(config)
		if err != nil {
			return fmt.Errorf("error while dialing %s: %w", config.GRPCAddr, err)
		}
		p.instances[config.GRPCAddr] = instance
	}

	refresh := func(ctx context.Context) {
		ctx, cancel := context.WithDeadline(ctx, time.Now().Add(p.config.RefreshTimeout))
		defer cancel()
		p.Refresh(ctx)
	}
	refresh(context.Background())
	p.d.Daemon.Track(&p.t, "inlet/bioris-observe")
	p.t.Go(func() error {
		ticker := time.NewTicker(p.config.Refresh)
		defer ticker.Stop()
		for {
			select {
			case <-p.t.Dying():
				return nil
			case <-ticker.C:
				refresh(p.t.Context(context.Background()))
			}
		}
	})

	return p.rib.Start()
}

// Dial dials a RIS instance.
func (p *Provider) Dial(config RISInstance) (*RISInstanceRuntime, error) {
	securityOption := grpc.WithTransportCredentials(insecure.NewCredentials())

	if config.GRPCSecure {
		config := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		securityOption = grpc.WithTransportCredentials(credentials.NewTLS(config))
	}
	backoff := backoff.DefaultConfig
	conn, err := grpc.Dial(config.GRPCAddr, securityOption,
		grpc.WithUnaryInterceptor(p.clientMetrics.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(p.clientMetrics.StreamClientInterceptor()),
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff}),
	)
	if err != nil {
		return nil, fmt.Errorf("error while dialing RIS %s: %w", config.GRPCAddr, err)
	}
	client := pb.NewRoutingInformationServiceClient(conn)
	if client == nil {
		conn.Close()
		return nil, fmt.Errorf("error while opening RIS client %s", config.GRPCAddr)
	}
	p.t.Go(func() error {
		var state connectivity.State = -1
		for {
			if !conn.WaitForStateChange(p.t.Context(context.Background()), state) {
				return nil
			}
			state = conn.GetState()
			p.metrics.risUp.WithLabelValues(config.GRPCAddr).Set(func() float64 {
				if state == connectivity.Ready {
					return 1
				}
				return 0
			}())
		}
	})
	p.active.Store(true)

	return &RISInstanceRuntime{
		config: config,
		client: client,
		conn:   conn,
	}, nil
}

// Refresh retrieves the list of routers
func (p *Provider) Refresh(ctx context.Context) {
	routers := make(map[netip.Addr][]*RISInstanceRuntime)
	for _, config := range p.config.RISInstances {
		instance := p.instances[config.GRPCAddr]
		r, err := instance.client.GetRouters(ctx, &pb.GetRoutersRequest{})
		if err != nil {
			p.r.Err(err).Msgf("error while getting routers from %s", config.GRPCAddr)
			continue
		}
		p.metrics.knownRouters.WithLabelValues(config.GRPCAddr).Set(0)
		for _, router := range r.GetRouters() {
			routerAddress, err := netip.ParseAddr(router.Address)
			if err != nil {
				p.r.Err(err).Msgf("error while parsing router address %s", router.Address)
				continue
			}
			routerAddress = netip.AddrFrom16(routerAddress.As16())
			routers[routerAddress] = append(routers[routerAddress], p.instances[config.GRPCAddr])

			p.metrics.knownRouters.WithLabelValues(config.GRPCAddr).Inc()
			p.metrics.routerChosenAgentIDMatch.WithLabelValues(config.GRPCAddr, router.Address)
			p.metrics.routerChosenFallback.WithLabelValues(config.GRPCAddr, router.Address)
		}
	}

	p.mu.Lock()
	p.routers = routers
	p.mu.Unlock()
}

// chooseRouter selects the router ID best suited for the given agent ip. It
// returns router ID and RIS instance.
func (p *Provider) chooseRouter(agent netip.Addr) (netip.Addr, *RISInstanceRuntime, error) {
	var chosenRis *RISInstanceRuntime
	chosenRouterID := netip.IPv4Unspecified()
	exactMatch := false
	// We try all routers
	for r := range p.routers {
		chosenRouterID = r
		// If we find an exact match of router id and agent ip, we are done
		if r == agent {
			exactMatch = true
			break
		}
		// If not, we are implicitly using the last router id we found
	}

	// Verify that an actual router was found
	if chosenRouterID.IsUnspecified() {
		return chosenRouterID, nil, errNoRouter
	}

	// Randomly select a ris providing the router ID we selected earlier.
	// In the future, we might also want to exclude currently unavailable ris instances
	chosenRis = p.routers[chosenRouterID][rand.Intn(len(p.routers[chosenRouterID]))]

	if chosenRis == nil || chosenRouterID.IsUnspecified() {
		return chosenRouterID, nil, errNoInstance
	}

	// Update metrics with the chosen router/ris combination
	if exactMatch {
		p.metrics.routerChosenAgentIDMatch.WithLabelValues(chosenRis.config.GRPCAddr, chosenRouterID.Unmap().String()).Inc()
	} else {
		p.metrics.routerChosenFallback.WithLabelValues(chosenRis.config.GRPCAddr, chosenRouterID.Unmap().String()).Inc()
	}

	return chosenRouterID, chosenRis, nil
}

func (p *Provider) ObserveRIB(exporterAddress netip.Addr, ipv6 bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	afi := uint16(bgp.AFI_IP)
	safi := uint8(bgp.SAFI_UNICAST)
	subscriptionKey := fmt.Sprintf("%s-%d-%d", exporterAddress, afi, safi)

	if _, has := p.observeSubscriptions[subscriptionKey]; has {
		return nil
	}

	_, ris, err := p.chooseRouter(exporterAddress)
	if err != nil {
		return err
	}

	requestAfiSafi := pb.ObserveRIBRequest_IPv4Unicast
	if ipv6 {
		afi = uint16(bgp.AFI_IP6)
		requestAfiSafi = pb.ObserveRIBRequest_IPv6Unicast
	}

	cli, err := ris.client.ObserveRIB(p.t.Context(context.Background()), &pb.ObserveRIBRequest{
		Router:          exporterAddress.String(),
		Afisafi:         requestAfiSafi,
		AllowUnreadyRib: true,
		VrfId:           ris.config.VRFId,
		Vrf:             ris.config.VRF,
	})

	if err != nil {
		return err
	}
	p.metrics.runningObserveRIB.WithLabelValues(ris.config.GRPCAddr).Add(1)

	p.observeSubscriptions[subscriptionKey] = cli

	exporterStr := exporterAddress.String()
	p.rib.AddPeer(exporterAddress)
	p.t.Go(func() error {
		p.r.Info().Msgf("starting ObserveRIB subscription for %s", exporterStr)
		defer func() {
			p.metrics.runningObserveRIB.WithLabelValues(ris.config.GRPCAddr).Add(-1)
			// TODO: clean rib after timeout when the subscription ends
		}()
		for {
			select {
			case <-p.t.Dying():
				return cli.CloseSend()
			default:
			}
			update, err := cli.Recv()
			if err != nil {
				return err
			}
			route := update.GetRoute()
			prefix, err := netip.ParsePrefix(route.Pfx.Address.String())
			if err != nil {
				return err
			}
			p.metrics.streamedUpdates.WithLabelValues(ris.config.GRPCAddr, exporterStr).Inc()
			for _, path := range route.Paths {
				if path.GetType() != api.Path_BGP {
					continue
				}
				bgpPath := path.BgpPath
				nh, err := netip.ParseAddr(bgpPath.NextHop.String())
				if err != nil {
					return err
				}
				asPath := make([]uint32, len(bgpPath.AsPath))
				for i, as := range bgpPath.AsPath {
					// TODO: what to do with confed AS ?
					asPath[i] = as.Asns[0]
				}
				largeCommunities := make([]bgp.LargeCommunity, len(bgpPath.LargeCommunities))
				for i, comm := range bgpPath.LargeCommunities {
					largeCommunities[i] = *bgp.NewLargeCommunity(comm.GlobalAdministrator, comm.DataPart1, comm.DataPart2)
				}
				attrs := rib.RouteAttributes{
					ASPath:           asPath,
					Communities:      bgpPath.Communities,
					LargeCommunities: largeCommunities,
					Plen:             uint8(prefix.Bits()),
				}
				p.rib.AddRoute(
					exporterAddress,
					afi,
					safi,
					rib.RD(0),
					bgpPath.PathIdentifier,
					prefix.Addr(),
					prefix.Bits(),
					attrs,
					nh,
				)
			}
		}
	})
	return nil
}

// Stop closes connection to ris
func (p *Provider) Stop() error {
	defer func() {
		for _, v := range p.instances {
			if v.conn != nil {
				v.conn.Close()
			}
		}
		p.r.Info().Msg("BioRIS provider stopped")
	}()
	if err := p.rib.Stop(); err != nil {
		return err
	}
	p.r.Info().Msg("stopping BioRIS provider")
	p.t.Kill(nil)
	return p.t.Wait()
}
