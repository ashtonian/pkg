package gowalker

import (
	"errors"
	"reflect"
)

// Control governs traversal after a callback. Stop succeeds without emitting
// further callbacks. SkipChildren still emits a Leave when leave events are on.
type Control uint8

const (
	Continue Control = iota
	SkipChildren
	Stop
)

// VisitKind distinguishes entry/leave events from terminal recursion references.
// Reference is emitted by type walking; Cycle by value walking. Neither has a
// paired Leave. Stop or callback errors can leave ancestor entries unpaired.
type VisitKind uint8

const (
	Enter VisitKind = iota
	Leave
	Reference
	Cycle
)

// WalkLimits bound one traversal. Root depth is zero. MaxNodes counts attempted
// entries, references and cycles, including nils; Leave events are not counted.
// MaxDepth may be zero (root only); MaxNodes must be positive. No unlimited mode
// is provided. A limit error occurs before the over-budget node's callback.
type WalkLimits struct{ MaxDepth, MaxNodes int }

// DefaultWalkLimits returns a copy of the depth-128, 100000-node walk budget.
func DefaultWalkLimits() WalkLimits { return WalkLimits{MaxDepth: 128, MaxNodes: 100000} }

type walkSettings struct {
	limits    WalkLimits
	leave     bool
	unordered bool
	less      func(reflect.Value, reflect.Value) bool
}

// WalkOption configures a single traversal, without changing the engine's view.
type WalkOption func(*walkSettings) error

// WithWalkLimits replaces the complete depth/node budget for one traversal.
func WithWalkLimits(limits WalkLimits) WalkOption {
	return func(s *walkSettings) error {
		if limits.MaxDepth < 0 || limits.MaxNodes < 1 {
			return ErrLimit
		}
		s.limits = limits
		return nil
	}
}

// WithLeaveEvents adds Leave after ordinary entries, including pruned nodes.
// References and cycles remain terminal events without a Leave.
func WithLeaveEvents() WalkOption { return func(s *walkSettings) error { s.leave = true; return nil } }

// WithUnorderedMaps permits MapRange order for non-string-keyed maps. String-keyed
// maps remain sorted. This option and WithMapKeyOrder are mutually exclusive.
func WithUnorderedMaps() WalkOption {
	return func(s *walkSettings) error { s.unordered = true; return nil }
}

// WithMapKeyOrder supplies a strict weak ordering for non-string map keys. String
// map keys use lexical order regardless of this option. The comparator must not
// mutate traversed values. Panics/side effects in callbacks are caller behavior.
func WithMapKeyOrder(less func(reflect.Value, reflect.Value) bool) WalkOption {
	return func(s *walkSettings) error {
		if less == nil {
			return ErrKey
		}
		s.less = less
		return nil
	}
}

func walkOptions(options []WalkOption) (walkSettings, error) {
	if len(options) == 0 {
		return walkSettings{limits: DefaultWalkLimits()}, nil
	}
	s := walkSettings{limits: DefaultWalkLimits()}
	for _, option := range options {
		if option == nil {
			return s, ErrType
		}
		if err := option(&s); err != nil {
			return s, err
		}
	}
	if s.unordered && s.less != nil {
		return s, ErrKey
	}
	return s, nil
}

var errWalkStopped = errors.New("walk stopped")

func controlError(control Control) error {
	switch control {
	case Continue, SkipChildren:
		return nil
	case Stop:
		return errWalkStopped
	default:
		return ErrType
	}
}
