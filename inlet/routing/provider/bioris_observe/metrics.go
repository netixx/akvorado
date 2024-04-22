// SPDX-FileCopyrightText: 2024 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package biorisobserve

import "akvorado/common/reporter"

type metrics struct {
	risUp                    *reporter.GaugeVec
	knownRouters             *reporter.GaugeVec
	runningObserveRIB        *reporter.GaugeVec
	streamedUpdates          *reporter.CounterVec
	streamedPaths         	*reporter.CounterVec
}

// initMetrics initialize the metrics for the BMP component.
func (p *Provider) initMetrics() {
	p.r.MetricCollector(p.clientMetrics)
	p.metrics.risUp = p.r.GaugeVec(
		reporter.GaugeOpts{
			Name: "connection_up",
			Help: "Connection to BioRIS instance up.",
		},
		[]string{"ris"},
	)
	p.metrics.knownRouters = p.r.GaugeVec(
		reporter.GaugeOpts{
			Name: "known_routers_total",
			Help: "Number of known routers per RIS.",
		},
		[]string{"ris"},
	)
	p.metrics.runningObserveRIB = p.r.GaugeVec(
		reporter.GaugeOpts{
			Name: "observe_rib_total",
			Help: "Number currently running observeRIB subscriptions.",
		},
		[]string{"ris"},
	)
	p.metrics.streamedUpdates = p.r.CounterVec(
		reporter.CounterOpts{
			Name: "update_total",
			Help: "Cumulative count of all updates received from ris instance for a given router.",
		},
		[]string{"ris", "router"},
	)
	p.metrics.streamedPaths = p.r.CounterVec(
		reporter.CounterOpts{
			Name: "update_total",
			Help: "Cumulative count of all updates received from ris instance for a given router.",
		},
		[]string{"ris", "router"},
	)
}
