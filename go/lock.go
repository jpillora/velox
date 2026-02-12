package velox

import "sync"

// RLocker extends sync.Locker with read-lock methods.
// If a struct only implements sync.Locker, velox falls back to Lock() for reads.
type RLocker interface {
	sync.Locker
	RLock()
	RUnlock()
}
