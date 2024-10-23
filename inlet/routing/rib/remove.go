// SPDX-FileCopyrightText: 2022 Free Mobile
// SPDX-License-Identifier: AGPL-3.0-only

package rib

import (
	"context"
	"time"
)

func (p *Provider) peerRemovalWorker() error {
	for {
		select {
		case <-p.t.Dying():
			return nil
		case pkey := <-p.peerRemovalChan:
			exporterStr := pkey.Unmap().String()
			for {
				// Do one run of removal (read/write lock)
				_, done, duplicate := func() (int, bool, bool) {
					start := p.d.Clock.Now()
					ctx, cancel := context.WithTimeout(p.t.Context(context.Background()),
						p.config.RIBPeerRemovalMaxTime)
					p.mu.Lock()
					defer func() {
						cancel()
						p.mu.DowngradeLock()
						p.metrics.locked.WithLabelValues("peer-removal").Observe(
							float64(p.d.Clock.Now().Sub(start).Nanoseconds()) / 1000 / 1000 / 1000)
					}()
					p.mu.RLock()
					pinfo := p.peers[pkey]
					p.mu.RUnlock()
					if pinfo == nil {
						// Already removed (removal can be queued several times)
						return 0, true, true
					}
					removed, done := p.rib.flushPeerContext(ctx, pinfo.reference,
						p.config.RIBPeerRemovalBatchRoutes)
					if done {
						p.mu.Lock()
						// Run was complete, remove the peer (we need the lock)
						delete(p.peers, pkey)
						p.mu.Unlock()
					}
					return removed, done, false
				}()

				// Update stats and optionally sleep (read lock)
				func() {
					defer p.mu.RUnlock()
					// p.metrics.routes.WithLabelValues(exporterStr).Sub(float64(removed))
					if done {
						// Run was complete, update metrics
						if !duplicate {
							p.metrics.peers.WithLabelValues(exporterStr).Dec()
							p.metrics.peerRemovalDone.WithLabelValues(exporterStr).Inc()
						}
						return
					}
					// Run is incomplete, update metrics and sleep a bit
					p.metrics.peerRemovalPartial.WithLabelValues(exporterStr).Inc()
					select {
					case <-p.t.Dying():
						done = true
					case <-time.After(p.config.RIBPeerRemovalSleepInterval):
					}
				}()
				if done {
					break
				}
			}
		}
	}
}
