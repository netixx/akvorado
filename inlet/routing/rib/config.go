package rib

import (
	"time"
)

type Configuration struct {
	// RDs list the RDs to keep. If none are specified, all
	// received routes are processed. 0 match an absence of RD.
	RDs []RD
	// CollectASNs is true when we want to collect origin AS numbers
	CollectASNs bool
	// CollectASPaths is true when we want to collect AS paths
	CollectASPaths bool
	// CollectCommunities is true when we want to collect communities
	CollectCommunities bool
	// RIBPeerRemovalMaxTime tells the maximum time the removal worker should run to remove a peer
	RIBPeerRemovalMaxTime time.Duration `validate:"min=10ms"`
	// RIBPeerRemovalSleepInterval tells how much time to sleep between two runs of the removal worker
	RIBPeerRemovalSleepInterval time.Duration `validate:"min=10ms"`
	// RIBPeerRemovalMaxQueue tells how many pending removal requests to keep
	RIBPeerRemovalMaxQueue int `validate:"min=1"`
	// RIBPeerRemovalBatchRoutes tells how many routes to remove before checking
	// if we have a higher priority request. This is only if RIB is in memory
	// mode.
	RIBPeerRemovalBatchRoutes int `validate:"min=1"`
}

// DefaultConfiguration represents the default configuration for the BMP server
func DefaultConfiguration() Configuration {
	return Configuration{
		CollectASNs:                 true,
		CollectASPaths:              true,
		CollectCommunities:          true,
		RIBPeerRemovalMaxTime:       100 * time.Millisecond,
		RIBPeerRemovalSleepInterval: 500 * time.Millisecond,
		RIBPeerRemovalMaxQueue:      10000,
		RIBPeerRemovalBatchRoutes:   5000,
	}
}
