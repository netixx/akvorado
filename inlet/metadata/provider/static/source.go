package static

import (
	"akvorado/common/helpers"
	"akvorado/common/remotedatasourcefetcher"
	"context"
)

type ExporterInfo struct {
	ExporterSubnet        string
	ExporterConfiguration `mapstructure:",squash"`
}

// initStaticExporters initializes the reconciliation map for exporter configurations
// with the static prioritized data from exporters' Configuration.
func (p *Provider) initStaticExporters() {
	staticExportersMap := p.exporters.ToMap()
	staticExporters := make([]ExporterInfo, 0, len(staticExportersMap))
	for subnet, config := range staticExportersMap {
		staticExporters = append(
			staticExporters,
			ExporterInfo{
				ExporterSubnet:        subnet,
				ExporterConfiguration: config,
			},
		)
	}
	p.exportersMap["static"] = staticExporters
}

func (p *Provider) UpdateRemoteDataSource(ctx context.Context, name string, source remotedatasourcefetcher.RemoteDataSource) (int, error) {
	results, err := p.exporterSourcesFetcher.Fetch(ctx, name, source)
	p.r.Info().Msgf("received results: %+v", results)
	if err != nil {
		return 0, err
	}
	finalMap := map[string]ExporterConfiguration{}
	p.exportersLock.Lock()
	p.exportersMap[name] = results
	p.exportersLock.Unlock()
	for id, results := range p.exportersMap {
		if id == "static" {
			continue
		}
		for _, exporterData := range results {
			// Concurrency for same Exporter config across multiple remote data sources is not handled
			finalMap[exporterData.ExporterSubnet] = exporterData.ExporterConfiguration
		}
	}
	for _, exporterData := range p.exportersMap["static"] {
		// This overrides duplicates config for an Exporter if it's also defined as static
		finalMap[exporterData.ExporterSubnet] = exporterData.ExporterConfiguration
	}
	p.exportersLock.Lock()
	p.exporters, err = helpers.NewSubnetMap[ExporterConfiguration](finalMap)
	if err != nil {
		return 0, err
	}
	p.exportersLock.Unlock()
	return len(results), nil
}
