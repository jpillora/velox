package velox

import (
	"reflect"
	"sync"
)

// bindable is implemented by VMap/VSlice (internal interface)
type bindable interface {
	bind(locker sync.Locker, pusher Pusher)
}

// BindAll walks a struct and binds all VMap/VSlice fields.
// Called by user constructors and velox client after unmarshal.
func BindAll(v any, locker sync.Locker, pusher Pusher) {
	bindValue(reflect.ValueOf(v), locker, pusher)
}

func bindValue(v reflect.Value, locker sync.Locker, pusher Pusher) {
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
			bindValue(v.Elem(), locker, pusher)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			// Only process exported fields that can be interfaced
			if field.CanInterface() || (field.CanAddr() && field.Addr().CanInterface()) {
				bindValue(field, locker, pusher)
			}
		}
	}
}
