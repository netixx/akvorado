package static

import (
	"context"

	"akvorado/common/helpers"
	"akvorado/common/remotedatasourcefetcher"
	"akvorado/inlet/metadata/provider"
)

type ExporterInfo struct {
	ExporterSubnet string

	Name string `validate:"required"`
	// Default is used if not empty for any unknown ifindexes
	Default provider.Interface `validate:"omitempty"`
	// IfIndexes is a map from interface indexes to interfaces
	Interfaces []ExporterInterface `validate:"omitempty"`
}

type ExporterInterface struct {
	IfIndex            uint
	provider.Interface `validate:"omitempty,dive" mapstructure:",squash"`
}

func (i ExporterInfo) toExporterConfiguration() ExporterConfiguration {
	ifindexMap := map[uint]provider.Interface{}
	for _, iface := range i.Interfaces {
		ifindexMap[iface.IfIndex] = iface.Interface
	}

	return ExporterConfiguration{
		Name:      i.Name,
		Default:   i.Default,
		IfIndexes: ifindexMap,
	}
}

// initStaticExporters initializes the reconciliation map for exporter configurations
// with the static prioritized data from exporters' Configuration.
func (p *Provider) initStaticExporters() {
	staticExportersMap := p.exporters.ToMap()
	staticExporters := make([]ExporterInfo, 0, len(staticExportersMap))
	for subnet, config := range staticExportersMap {
		interfaces := make([]ExporterInterface, 0, len(config.IfIndexes))
		for ifindex, iface := range config.IfIndexes {
			interfaces = append(interfaces, ExporterInterface{
				IfIndex:   ifindex,
				Interface: iface,
			})
		}
		staticExporters = append(
			staticExporters,
			ExporterInfo{
				ExporterSubnet: subnet,
				Name:           config.Name,
				Default:        config.Default,
				Interfaces:     interfaces,
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
			finalMap[exporterData.ExporterSubnet] = exporterData.toExporterConfiguration()
		}
	}
	for _, exporterData := range p.exportersMap["static"] {
		// This overrides duplicates config for an Exporter if it's also defined as static
		finalMap[exporterData.ExporterSubnet] = exporterData.toExporterConfiguration()
	}
	p.exportersLock.Lock()
	p.exporters, err = helpers.NewSubnetMap[ExporterConfiguration](finalMap)
	if err != nil {
		return 0, err
	}
	p.exportersLock.Unlock()
	return len(results), nil
}
