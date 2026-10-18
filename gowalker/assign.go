package gowalker

import (
	"math"
	"math/big"
	"reflect"
)

func assignment(input reflect.Value, target reflect.Type, checked bool) (reflect.Value, error) {
	if !input.IsValid() {
		return reflect.Value{}, ErrType
	}
	if !input.CanInterface() {
		return reflect.Value{}, ErrAccess
	}
	if input.Type().AssignableTo(target) {
		return input, nil
	}
	if !checked {
		return reflect.Value{}, ErrType
	}
	if !numeric(input.Kind()) || !numeric(target.Kind()) || !input.Type().ConvertibleTo(target) {
		return reflect.Value{}, ErrConversion
	}
	before, ok := exactNumber(input)
	if !ok {
		return reflect.Value{}, ErrConversion
	}
	// Numeric conversions are defined even when they overflow or round. Compare
	// exact values before accepting the prepared result; round trips through a
	// float64 would lose information for large integers.
	converted := input.Convert(target)
	after, ok := exactNumber(converted)
	if !ok || before.real.Cmp(after.real) != 0 || before.imag.Cmp(after.imag) != 0 || before.realNegativeZero != after.realNegativeZero || before.imagNegativeZero != after.imagNegativeZero {
		return reflect.Value{}, ErrConversion
	}
	return converted, nil
}

func numeric(kind reflect.Kind) bool {
	return kind >= reflect.Int && kind <= reflect.Complex128
}

type number struct {
	real, imag                         *big.Rat
	realNegativeZero, imagNegativeZero bool
}

func exactNumber(v reflect.Value) (number, bool) {
	n := number{real: new(big.Rat), imag: new(big.Rat)}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n.real.SetInt64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n.real.SetInt(new(big.Int).SetUint64(v.Uint()))
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if n.real.SetFloat64(f) == nil {
			return number{}, false
		}
		n.realNegativeZero = f == 0 && math.Signbit(f)
	case reflect.Complex64, reflect.Complex128:
		c := v.Complex()
		if n.real.SetFloat64(real(c)) == nil || n.imag.SetFloat64(imag(c)) == nil {
			return number{}, false
		}
		n.realNegativeZero = real(c) == 0 && math.Signbit(real(c))
		n.imagNegativeZero = imag(c) == 0 && math.Signbit(imag(c))
	default:
		return number{}, false
	}
	return n, true
}
