// SPDX-FileCopyrightText: 2022 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package rib

import "akvorado/common/reporter"

type metrics struct {
	peers                *reporter.GaugeVec
	routes               *reporter.GaugeVec
	ignored              *reporter.CounterVec
	locked               *reporter.SummaryVec
	peerRemovalDone      *reporter.CounterVec
	peerRemovalPartial   *reporter.CounterVec
	peerRemovalQueueFull *reporter.CounterVec
}

// initMetrics initialize the metrics for the BMP component.
func (p *Provider) initMetrics() {
	p.metrics.peers = p.r.GaugeVec(
		reporter.GaugeOpts{
			Name: "peers_total",
			Help: "Number of peers in RIB.",
		},
		[]string{"exporter"},
	)
	p.metrics.routes = p.r.GaugeVec(
		reporter.GaugeOpts{
			Name: "routes_total",
			Help: "Number of routes in RIB.",
		},
		[]string{"exporter"},
	)
	p.metrics.ignored = p.r.CounterVec(
		reporter.CounterOpts{
			Name: "ignored_updates_total",
			Help: "Number of ignored BGP updates.",
		},
		[]string{"exporter", "reason", "error"},
	)
	p.metrics.locked = p.r.SummaryVec(
		reporter.SummaryOpts{
			Name:       "locked_duration_seconds",
			Help:       "Duration during which the RIB is locked.",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"reason"},
	)
	p.metrics.peerRemovalDone = p.r.CounterVec(
		reporter.CounterOpts{
			Name: "removed_peers_total",
			Help: "Number of peers removed from the RIB.",
		},
		[]string{"exporter"},
	)
	p.metrics.peerRemovalPartial = p.r.CounterVec(
		reporter.CounterOpts{
			Name: "removed_partial_peers_total",
			Help: "Number of peers partially removed from the RIB.",
		},
		[]string{"exporter"},
	)
	p.metrics.peerRemovalQueueFull = p.r.CounterVec(
		reporter.CounterOpts{
			Name: "removal_queue_full_total",
			Help: "Number of time the removal queue was full.",
		},
		[]string{"exporter"},
	)
}
