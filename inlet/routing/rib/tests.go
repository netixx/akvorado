//go:build !release

package rib

// Reduce hash mask to generate collisions during tests (this should
// be optimized out by the compiler)
const rtaHashMask = 0xff

// Use a predictable seed for tests.
var rtaHashSeed = uint64(0)
