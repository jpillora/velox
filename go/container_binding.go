package velox

import "sync"

// containerBinding is published atomically because readers acquire the state
// lock through it. Each operation must release the same lock it acquired, even
// if the container is rebound while that operation is waiting for the lock.
type containerBinding struct {
	locker sync.Locker
	pusher Pusher
}

func (b *containerBinding) rlock() func() {
	if b == nil || b.locker == nil {
		return func() {}
	}
	if rl, ok := b.locker.(RLocker); ok {
		rl.RLock()
		return rl.RUnlock
	}
	b.locker.Lock()
	return b.locker.Unlock
}

func (b *containerBinding) lock() func() {
	if b == nil || b.locker == nil {
		return func() {}
	}
	b.locker.Lock()
	return b.locker.Unlock
}
