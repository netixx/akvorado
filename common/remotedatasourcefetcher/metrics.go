// SPDX-FileCopyrightText: 2024 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package remotedatasourcefetcher

import (
	"akvorado/common/reporter"
	"sync"
)

type metrics struct {
	remoteDataSourceUpdates *reporter.CounterVec
	remoteDataSourceErrors  *reporter.CounterVec
	remoteDataSourceCount   *reporter.GaugeVec
}

var initMetricsOnce sync.Once
var componentMetrics = metrics{}

func initMetrics(r *reporter.Reporter) {
	componentMetrics.remoteDataSourceUpdates = r.CounterVec(
		reporter.CounterOpts{
			Name: "updates_total",
			Help: "Number of successful updates for a remote data source",
		},
		[]string{"type", "source"},
	)
	componentMetrics.remoteDataSourceErrors = r.CounterVec(
		reporter.CounterOpts{
			Name: "errors_total",
			Help: "Number of failed updates for a remote data source",
		},
		[]string{"type", "source", "error"},
	)
	componentMetrics.remoteDataSourceCount = r.GaugeVec(
		reporter.GaugeOpts{
			Name: "data_total",
			Help: "Number of objects imported from a given source",
		},
		[]string{"type", "source"},
	)
}

