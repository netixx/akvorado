// SPDX-FileCopyrightText: 2022 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

// Package clickhouse handles configuration of the ClickHouse database.
package clickhouse

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"akvorado/common/remotedatasourcefetcher"

	"github.com/cenkalti/backoff/v4"
	"gopkg.in/tomb.v2"

	"akvorado/common/clickhousedb"
	"akvorado/common/daemon"
	"akvorado/common/httpserver"
	"akvorado/common/reporter"
	"akvorado/common/schema"
)

// Component represents the ClickHouse configurator.
type Component struct {
	r       *reporter.Reporter
	d       *Dependencies
	t       tomb.Tomb
	config  Configuration
	metrics metrics

	migrationsDone        chan bool // closed when migrations are done
	migrationsOnce        chan bool // closed after first attempt to migrate
	networkSourcesFetcher *remotedatasourcefetcher.Component[externalNetworkAttributes]
	networkSources        map[string][]externalNetworkAttributes
	networkSourcesLock    sync.RWMutex
}

// Dependencies define the dependencies of the ClickHouse configurator.
type Dependencies struct {
	Daemon     daemon.Component
	HTTP       *httpserver.Component
	ClickHouse *clickhousedb.Component
	Schema     *schema.Component
}

// New creates a new ClickHouse component.
func New(r *reporter.Reporter, configuration Configuration, dependencies Dependencies) (*Component, error) {

	c := Component{
		r:              r,
		d:              &dependencies,
		config:         configuration,
		migrationsDone: make(chan bool),
		migrationsOnce: make(chan bool),
		networkSources: make(map[string][]externalNetworkAttributes),
	}
	var err error
	c.networkSourcesFetcher, err = remotedatasourcefetcher.New[externalNetworkAttributes](r, &c, "network_source", configuration.NetworkSources)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize remote data source fetcher component: %w", err)
	}
	c.initMetrics()
	if err := c.registerHTTPHandlers(); err != nil {
		return nil, err
	}

	// Ensure resolutions are sorted and we have a 0-interval resolution first.
	sort.Slice(c.config.Resolutions, func(i, j int) bool {
		return c.config.Resolutions[i].Interval < c.config.Resolutions[j].Interval
	})
	if len(c.config.Resolutions) == 0 || c.config.Resolutions[0].Interval != 0 {
		c.config.Resolutions = append([]ResolutionConfiguration{}, c.config.Resolutions...)
	}

	return &c, nil
}

// Start the ClickHouse component.
func (c *Component) Start() error {
	c.r.Info().Msg("starting ClickHouse component")

	// Database migration
	migrationsOnce := false
	c.metrics.migrationsRunning.Set(1)
	c.t.Go(func() error {
		customBackoff := backoff.NewExponentialBackOff()
		customBackoff.MaxElapsedTime = 0
		customBackoff.InitialInterval = time.Second
		for {
			if !c.config.SkipMigrations {
				c.r.Info().Msg("attempting database migration")
				if err := c.migrateDatabase(); err != nil {
					c.r.Err(err).Msg("database migration error")
				} else {
					return nil
				}
				if !migrationsOnce {
					close(c.migrationsOnce)
					migrationsOnce = true
					customBackoff.Reset()
				}
			}
			next := customBackoff.NextBackOff()
			select {
			case <-c.t.Dying():
				return nil
			case <-time.Tick(next):
			}
		}
	})

	// Network sources update
	if err := c.networkSourcesFetcher.Start(); err != nil {
		return fmt.Errorf("unable to start network sources fetcher component: %w", err)
	}

	return nil
}

// Stop stops the ClickHouse component.
func (c *Component) Stop() error {
	c.r.Info().Msg("stopping ClickHouse component")
	defer c.r.Info().Msg("ClickHouse component stopped")
	c.t.Kill(nil)
	return c.t.Wait()
}
