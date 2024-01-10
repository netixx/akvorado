// SPDX-FileCopyrightText: 2023 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

// Package static is a metadata provider using static configuration to answer to
// requests.
package static

import (
	"context"
	"fmt"
	"sync"

	"akvorado/common/helpers"
	"akvorado/common/remotedatasourcefetcher"

	"akvorado/common/reporter"
	"akvorado/inlet/metadata/provider"
)

// Provider represents the static provider.
type Provider struct {
	r                      *reporter.Reporter
	exporterSourcesFetcher *remotedatasourcefetcher.Component[ExporterInfo]
	exportersMap           map[string][]ExporterInfo
	exporters              *helpers.SubnetMap[ExporterConfiguration]
	exportersLock          sync.RWMutex
	put                    func(provider.Update)
}

// New creates a new static provider from configuration
func (configuration Configuration) New(r *reporter.Reporter, put func(provider.Update)) (provider.Provider, error) {
	p := &Provider{
		r:            r,
		exportersMap: map[string][]ExporterInfo{},
		exporters:    configuration.Exporters,
		put:          put,
	}
	p.initStaticExporters()
	var err error
	p.exporterSourcesFetcher, err = remotedatasourcefetcher.New[ExporterInfo](r, p, "metadata", configuration.ExporterSources)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize remote data source fetcher component: %w", err)
	}
	if err := p.exporterSourcesFetcher.Start(); err != nil {
		return nil, fmt.Errorf("unable to start network sources fetcher component: %w", err)
	}
	return p, nil
}

// Query queries static configuration.
func (p *Provider) Query(_ context.Context, query provider.BatchQuery) error {
	exporter, ok := p.exporters.Lookup(query.ExporterIP)
	if !ok {
		return nil
	}
	for _, ifIndex := range query.IfIndexes {
		iface, ok := exporter.IfIndexes[ifIndex]
		if !ok {
			iface = exporter.Default
		}
		p.put(provider.Update{
			Query: provider.Query{
				ExporterIP: query.ExporterIP,
				IfIndex:    ifIndex,
			},
			Answer: provider.Answer{
				ExporterName: exporter.Name,
				Interface:    iface,
			},
		})
	}
	return nil
}
