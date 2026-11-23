// Package deepcopy copies config values so that nothing the copy holds can
// be changed through the original. config copies defaults, provider data and
// configs between pipeline stages with it.
//
// The config/kongprovider module is to copy flag targets and the published
// config with it once it requires the first config release that ships this
// package; until then it keeps a copy of its own. kongprovider is a separate
// module, so once it imports this package, every kongprovider release must
// build against every later config release: Value's signature and the
// contract below are a cross-module API that changes only
// source-compatibly.
//
// The copy owns every map, slice, array element, pointer target and
// interface value it can reach through exported fields — including the
// exported fields an unexported embedded struct promotes, as decoding fills
// them. Unexported fields beyond that, channels and functions are opaque
// state and keep the value the original holds; a type built from such state,
// as time.Time is, copies as its plain value does. Data the original shares
// internally, cycles included, is shared the same way inside the copy.
package deepcopy

import "reflect"

// Value returns a copy of v that shares nothing settable with it.
func Value(v reflect.Value) reflect.Value {
	c := copier{made: map[key]reflect.Value{}}
	return c.copy(v)
}

type copier struct {
	made map[key]reflect.Value
}

// key identifies a map, slice or pointer already copied. A slice's length is
// part of it: two slices over one array are different values.
type key struct {
	ptr uintptr
	len int
	typ reflect.Type
}

func (c copier) copy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return v
		}
		k := key{ptr: v.Pointer(), typ: v.Type()}
		if v.Kind() == reflect.Slice {
			k.len = v.Len()
		}
		if done, ok := c.made[k]; ok {
			return done
		}
		switch v.Kind() {
		case reflect.Pointer:
			p := reflect.New(v.Type().Elem())
			c.made[k] = p
			p.Elem().Set(c.copy(v.Elem()))
			return p
		case reflect.Map:
			m := reflect.MakeMapWithSize(v.Type(), v.Len())
			c.made[k] = m
			for it := v.MapRange(); it.Next(); {
				m.SetMapIndex(c.copy(it.Key()), c.copy(it.Value()))
			}
			return m
		default:
			s := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			c.made[k] = s
			for i := range v.Len() {
				s.Index(i).Set(c.copy(v.Index(i)))
			}
			return s
		}
	case reflect.Array:
		a := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			a.Index(i).Set(c.copy(v.Index(i)))
		}
		return a
	case reflect.Struct:
		s := reflect.New(v.Type()).Elem()
		s.Set(v)
		c.detach(s, v)
		return s
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		i := reflect.New(v.Type()).Elem()
		i.Set(c.copy(v.Elem()))
		return i
	}
	return v
}

// detach gives s, a plain copy of the struct v, a copy of its own of every
// field it can set: each exported field, and the exported fields an
// unexported embedded struct promotes, at any depth. reflect lets those be
// set because they are reachable by promotion; any other unexported field
// keeps the value v holds.
func (c copier) detach(s, v reflect.Value) {
	for i := range v.NumField() {
		f := v.Type().Field(i)
		switch {
		case f.IsExported():
			s.Field(i).Set(c.copy(v.Field(i)))
		case f.Anonymous && f.Type.Kind() == reflect.Struct:
			c.detach(s.Field(i), v.Field(i))
		}
	}
}
