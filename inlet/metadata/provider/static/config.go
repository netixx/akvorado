// SPDX-FileCopyrightText: 2022 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package static

import (
	"akvorado/common/helpers"
	"akvorado/common/remotedatasourcefetcher"
	"akvorado/inlet/metadata/provider"
	"time"
)

// Configuration describes the configuration for the static provider
type Configuration struct {
	// Exporters is a subnet map matching Exporters to their configuration
	Exporters *helpers.SubnetMap[ExporterConfiguration] `validate:"omitempty,dive"`
	// ExporterSources defines a set of remote Exporters
	// definitions to map IP address to their configuration.
	// The results are overridden by the content of Exporters.
	ExporterSources map[string]remotedatasourcefetcher.RemoteDataSource `validate:"dive"`
	// ExporterSourcesTimeout tells how long to wait for exporter
	// sources to be ready. 503 is returned when not.
	ExporterSourcesTimeout time.Duration `validate:"min=0"`
}

// ExporterConfiguration is the interface configuration for an exporter.
type ExporterConfiguration struct {
	// Name is the name of the exporter
	Name string `validate:"required"`
	// Default is used if not empty for any unknown ifindexes
	Default provider.Interface `validate:"omitempty"`
	// IfIndexes is a map from interface indexes to interfaces
	IfIndexes map[uint]provider.Interface `validate:"omitempty,dive"`
}

// DefaultConfiguration represents the default configuration for the static provider
func DefaultConfiguration() provider.Configuration {
	return Configuration{
		Exporters: helpers.MustNewSubnetMap(map[string]ExporterConfiguration{}),
	}
}

func init() {
	helpers.RegisterMapstructureUnmarshallerHook(helpers.SubnetMapUnmarshallerHook[ExporterConfiguration]())
	helpers.RegisterSubnetMapValidation[ExporterConfiguration]()
}
