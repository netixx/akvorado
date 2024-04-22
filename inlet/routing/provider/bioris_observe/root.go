// SPDX-FileCopyrightText: 2024 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package biorisobserve

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/bio-routing/bio-rd/cmd/ris/api"
	bnet "github.com/bio-routing/bio-rd/net"
	"github.com/bio-routing/bio-rd/net/api"
	routeapi "github.com/bio-routing/bio-rd/route/api"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/osrg/gobgp/v3/pkg/packet/bgp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
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

type observeRIBRequest struct {
	exporterAddress netip.Addr
	ipv6 bool
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
	observeRIBRequestChan chan observeRIBRequest
	observeRequests map[string]struct{}
	observeRequestsMu sync.RWMutex

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
		observeRIBRequestChan: make(chan observeRIBRequest),
		observeRequests : map[string]struct{}{},
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

	p.t.Go(func() error {
		for {
			select {
			case <- p.t.Dying():
				return nil
			case observeRequest := <- p.observeRIBRequestChan:
				if err := p.observeRIB(observeRequest); err != nil {
					p.r.Err(err).Msg("failed to observeRIB")
				}
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
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time: 10*time.Second,
			Timeout: 1*time.Minute,
			PermitWithoutStream: true,
		}),
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
		}
	}

	p.mu.Lock()
	p.routers = routers
	p.mu.Unlock()
}

// chooseRouter selects the router ID best suited for the given agent ip. It
// returns router ID and RIS instance.
func (p *Provider) chooseRouter(agent netip.Addr) (netip.Addr, []*RISInstanceRuntime, error) {
	runtime, has := p.routers[agent]

	if has {
		return agent, runtime, nil
	}
	return agent, nil, errNoRouter
	
	// var chosenRis *RISInstanceRuntime
	// chosenRouterID := netip.IPv4Unspecified()
	// exactMatch := false
	// // We try all routers
	// for r := range p.routers {
	// 	chosenRouterID = r
	// 	// If we find an exact match of router id and agent ip, we are done
	// 	if r == agent {
	// 		exactMatch = true
	// 		break
	// 	}
	// 	// If not, we are implicitly using the last router id we found
	// }

	// // Verify that an actual router was found
	// if chosenRouterID.IsUnspecified() {
	// 	return chosenRouterID, nil, errNoRouter
	// }

	// // Randomly select a ris providing the router ID we selected earlier.
	// // In the future, we might also want to exclude currently unavailable ris instances
	// chosenRis = p.routers[chosenRouterID][rand.Intn(len(p.routers[chosenRouterID]))]

	// if chosenRis == nil || chosenRouterID.IsUnspecified() {
	// 	return chosenRouterID, nil, errNoInstance
	// }

	// // Update metrics with the chosen router/ris combination
	// if exactMatch {
	// 	p.metrics.routerChosenAgentIDMatch.WithLabelValues(chosenRis.config.GRPCAddr, chosenRouterID.Unmap().String()).Inc()
	// } else {
	// 	p.metrics.routerChosenFallback.WithLabelValues(chosenRis.config.GRPCAddr, chosenRouterID.Unmap().String()).Inc()
	// }

	// return chosenRouterID, chosenRis, nil
}


func prefixFromPfx(pfx *api.Prefix) (netip.Prefix, error) {
	prefix := bnet.NewPrefixFromProtoPrefix(pfx)
	network, ok := netip.AddrFromSlice(prefix.Addr().Bytes())

	if !ok {
		return netip.Prefix{}, fmt.Errorf("invalid network address: %s", pfx.GetAddress())
	}
	return netip.PrefixFrom(network ,int(prefix.Len())), nil
}

func (r observeRIBRequest) key() string {
	return fmt.Sprintf("%s-%d", r.exporterAddress, r.afisafi())
}


func (r observeRIBRequest) afisafi() pb.ObserveRIBRequest_AFISAFI {
	if r.ipv6 {
		return pb.ObserveRIBRequest_IPv6Unicast
	}
	return pb.ObserveRIBRequest_IPv4Unicast
}

func (p *Provider) submitObserveRib(observeRequest observeRIBRequest) {

	p.observeRequestsMu.RLock()
	_, has := p.observeRequests[observeRequest.key()]
	p.observeRequestsMu.RUnlock()
	if has {
		return
	}
	p.observeRequestsMu.Lock()
	p.observeRequests[observeRequest.key()] = struct{}{}
	p.observeRequestsMu.Unlock()
	p.observeRIBRequestChan <- observeRequest
}

func (p *Provider) observeRIB(observeRequest observeRIBRequest) error {
	_, risInstances, err := p.chooseRouter(observeRequest.exporterAddress)
	if err != nil {
		return err
	}

	p.rib.AddPeer(observeRequest.exporterAddress)
	// find the instance that has our RIB
	for _, ris := range risInstances {
		subscriptionKey := fmt.Sprintf("%s-%s", observeRequest.key(), ris.config.GRPCAddr)
		p.mu.RLock()
		if _, has := p.observeSubscriptions[subscriptionKey]; has {
			p.mu.RUnlock()
			continue
		}
		p.mu.RUnlock()
		// not working
		c := p.t.Context(context.Background())
		cli, err := ris.client.ObserveRIB(c, &pb.ObserveRIBRequest{
			Router:          observeRequest.exporterAddress.Unmap().String(),
			Afisafi:         observeRequest.afisafi(),
			AllowUnreadyRib: true,
			VrfId:           ris.config.VRFId,
			Vrf:             ris.config.VRF,
		})

		if err != nil {
			p.r.Err(err).Msg("failed to start ObserveRIB query")
			p.observeRequestsMu.Lock()
			delete(p.observeRequests, observeRequest.key())
			p.observeRequestsMu.Unlock()
			continue
		}

		p.metrics.runningObserveRIB.WithLabelValues(ris.config.GRPCAddr).Add(1)
		p.mu.Lock()
		p.observeSubscriptions[subscriptionKey] = cli
		p.mu.Unlock()

		exporterStr := observeRequest.exporterAddress.Unmap().String()
		afi := uint16(bgp.AFI_IP)
	  safi := uint8(bgp.SAFI_UNICAST)

		if observeRequest.ipv6 {
			afi = uint16(bgp.AFI_IP6)
		}
		p.t.Go(func() error {
			p.r.Info().Msgf("starting ObserveRIB subscription for %s, afi %d", exporterStr, afi)
			defer func() {
				p.mu.Lock()
				delete(p.observeSubscriptions, subscriptionKey)
				p.mu.Unlock()
				// allow the subscription to restart
				// TODO: better retry mecanism ??
				p.observeRequestsMu.Lock()
				delete(p.observeRequests, observeRequest.key())
				p.observeRequestsMu.Unlock()
				p.metrics.runningObserveRIB.WithLabelValues(ris.config.GRPCAddr).Add(-1)
				// TODO: clean rib after timeout when the subscription ends
			}()
			for {
				select {
				case <-p.t.Dying():
					p.r.Info().Msg("ObserveRIB: tomb dying")
					return cli.CloseSend()
				default:
				}
				update, err := cli.Recv()
				if err != nil {
					p.r.Err(err).Msg("error during ObserveRIB query")
					return nil
				}
				processedUpdates, processedPaths := p.consumeRoute(update, observeRequest.exporterAddress, afi, safi)
				p.metrics.streamedUpdates.WithLabelValues(ris.config.GRPCAddr, exporterStr).Add(float64(processedUpdates))
				p.metrics.streamedPaths.WithLabelValues(ris.config.GRPCAddr, exporterStr).Add(float64(processedPaths))
			}
		})
	}
	return nil
}

func (p *Provider) consumeRoute(
	update *pb.RIBUpdate,
	exporterAddress netip.Addr,
	afi uint16,
	safi uint8,
) (int, int) {
	route := update.GetRoute()
	if route == nil {
		return 0, 0
	}

	prefix, err := prefixFromPfx(route.Pfx)
	if err != nil {
		p.r.Info().Msgf("ObserveRIB unable to parse prefix: %s", route.Pfx)
		return 0, 0
	}

	pathCount := 0
	for _, path := range route.Paths {
		if path.GetType() != routeapi.Path_BGP {
			continue
		}
		bgpPath := path.BgpPath
		bNh := bnet.IPFromProtoIP(bgpPath.NextHop)
		nh, ok := netip.AddrFromSlice(bNh.Bytes())
		if !ok {
			p.r.Info().Msgf("ObserveRIB unable to parse nexthop: %s", bgpPath.NextHop)
			continue
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
		pathCount += 1
	}
	return 1, pathCount
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
	close(p.observeRIBRequestChan)
	p.r.Info().Msg("stopping BioRIS provider")
	p.t.Kill(nil)
	return p.t.Wait()
}
