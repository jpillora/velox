package velox

import (
	"fmt"
	"reflect"
	"sync"
)

// bindable is implemented by VMap/VSlice (internal interface)
type bindable interface {
	bind(locker sync.Locker, pusher Pusher)
}

var (
	lockerType  = reflect.TypeOf((*sync.Locker)(nil)).Elem()
	mutexType   = reflect.TypeOf(sync.Mutex{})
	rwMutexType = reflect.TypeOf(sync.RWMutex{})
)

// bindAll walks a struct and binds all VMap/VSlice fields to
// the given locker and pusher. Called automatically by SyncHandler
// (server side) and Client (after unmarshal).
//
// Panics if any nested struct implements sync.Locker, since nested
// locks conflict with velox's global lock pattern. Use VMap/VSlice instead.
func bindAll(v any, locker sync.Locker, pusher Pusher) {
	bindValue(reflect.ValueOf(v), locker, pusher, true)
}

func bindValue(v reflect.Value, locker sync.Locker, pusher Pusher, root bool) {
	if !v.IsValid() {
		return
	}

	// Check if addressable and can get interface (only for exported fields)
	if v.CanAddr() && v.Addr().CanInterface() {
		if b, ok := v.Addr().Interface().(bindable); ok {
			b.bind(locker, pusher)
		}
	} else if v.CanInterface() {
		if b, ok := v.Interface().(bindable); ok {
			b.bind(locker, pusher)
		}
	}

	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			bindValue(v.Elem(), locker, pusher, root)
		}
	case reflect.Struct:
		t := v.Type()
		// Skip mutex types themselves — no bindable children
		if t == mutexType || t == rwMutexType {
			return
		}
		// Detect nested structs with their own lock. These conflict
		// with velox's global lock pattern — the parent lock protects
		// all state during marshal, so nested locks cause either
		// deadlocks or unprotected concurrent access.
		if !root && v.CanAddr() && v.Addr().CanInterface() {
			iface := v.Addr().Interface()
			_, isBind := iface.(bindable)
			_, isLock := iface.(sync.Locker)
			if isLock && !isBind {
				panic(fmt.Sprintf("velox: nested type %s implements sync.Locker; use VMap/VSlice with the parent lock instead", t))
			}
		}
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			// Only process exported fields that can be interfaced
			if field.CanInterface() || (field.CanAddr() && field.Addr().CanInterface()) {
				bindValue(field, locker, pusher, false)
			}
		}
	}
}
