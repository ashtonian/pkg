package config

import "reflect"

// deepCopy returns a copy of v sharing no map, slice, array element or
// pointer target with it. Data v shares internally, cycles included, is
// shared the same way in the copy.
func deepCopy(v reflect.Value) reflect.Value {
	c := copier{made: map[copyKey]reflect.Value{}}
	return c.copy(v)
}

type copier struct {
	made map[copyKey]reflect.Value
}

// copyKey identifies a map, slice or pointer already copied. A slice's
// length is part of it: two slices over one array are different values.
type copyKey struct {
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
		key := copyKey{ptr: v.Pointer(), typ: v.Type()}
		if v.Kind() == reflect.Slice {
			key.len = v.Len()
		}
		if done, ok := c.made[key]; ok {
			return done
		}
		switch v.Kind() {
		case reflect.Pointer:
			p := reflect.New(v.Type().Elem())
			c.made[key] = p
			p.Elem().Set(c.copy(v.Elem()))
			return p
		case reflect.Map:
			m := reflect.MakeMapWithSize(v.Type(), v.Len())
			c.made[key] = m
			for iter := v.MapRange(); iter.Next(); {
				m.SetMapIndex(c.copy(iter.Key()), c.copy(iter.Value()))
			}
			return m
		default:
			s := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			c.made[key] = s
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
		s.Set(v) // unexported fields cannot be reached, so they keep their values
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				s.Field(i).Set(c.copy(v.Field(i)))
			}
		}
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
