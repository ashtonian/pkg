package config

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cycNode struct {
	Name     string              `koanf:"name"`
	Next     *cycNode            `koanf:"next"`
	Prev     *cycNode            `koanf:"prev"`
	Children map[string]*cycNode `koanf:"children" validate:"dive"`
}

type cycSection struct {
	Port int         `koanf:"port"`
	Back *cycSection `koanf:"back"`
}

type cycCfg struct {
	Name    string      `koanf:"name"`
	Section *cycSection `koanf:"section"`
	Self    selfPointer `koanf:"-"`
	Any     any         `koanf:"-"`
	Chain   *cycNode    `koanf:"-"`
	Tree    *cycNode    `koanf:"-"`
	Parent  *cycCfg     `koanf:"-"`
	Valued  cycValuer   `koanf:"-"`
}

// cycValuer is validated as the node it returns.
type cycValuer struct{ node *cycNode }

func (v cycValuer) ValidatorValue() any { return v.node }

// A value holding a cycle on a path the default validator walks would keep
// it walking forever — spinning on a pointer that holds its own address,
// or recursing through a struct graph until the stack overflows and the
// process dies. The load refuses it with ErrValidation, naming the field
// where the cycle closes and the field it leads back to, and never the
// value.
func TestValidation_RefusesAValueHoldingACycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*cycCfg)
		want string
	}{
		{"pointer holding its own address", func(c *cycCfg) {
			var p selfPointer
			p = &p
			c.Self = p
		}, "Self holds a cycle back to Self"},
		{"interface holding a pointer to itself", func(c *cycCfg) {
			var a any
			a = &a
			c.Any = a
		}, "Any holds a cycle back to Any"},
		{"doubly linked structs", func(c *cycCfg) {
			a, b := &cycNode{Name: "a"}, &cycNode{Name: "b"}
			a.Next, b.Prev = b, a
			c.Chain = a
		}, "Chain.next.prev holds a cycle back to Chain"},
		{"map entry under a dive", func(c *cycCfg) {
			n := &cycNode{Name: "root"}
			n.Children = map[string]*cycNode{"self": n}
			c.Tree = n
		}, "Tree.children[self] holds a cycle back to Tree"},
		{"keyed section", func(c *cycCfg) {
			c.Section = &cycSection{Port: 1}
			c.Section.Back = c.Section
		}, "section.back holds a cycle back to section"},
		{"the config itself", func(c *cycCfg) { c.Parent = c }, "Parent holds a cycle back to $"},
		{"a validator value", func(c *cycCfg) {
			n := &cycNode{Name: "v"}
			n.Next = n
			c.Valued = cycValuer{node: n}
		}, "Valued.next holds a cycle back to Valued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load[cycCfg](t.Context(), WithPostLoad(func(_ context.Context, c *cycCfg) error {
				tc.set(c)
				return nil
			}))
			require.ErrorIs(t, err, ErrValidation)
			require.NotErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, err.Error(), "0x", "no address is shown")
		})
	}
}

type cycUnwalkedCfg struct {
	Name     string     `koanf:"name"`
	Skipped  *cycNode   `koanf:"-" validate:"-"`
	NoStruct *cycNode   `koanf:"-" validate:"nostructlevel"`
	Only     *cycNode   `koanf:"-" validate:"structonly"`
	List     []*cycNode `koanf:"-"`
	hidden   *cycNode
}

type cycSharedCfg struct {
	Name  string     `koanf:"name"`
	A     *cycNode   `koanf:"-"`
	B     *cycNode   `koanf:"-"`
	Nodes []*cycNode `koanf:"-" validate:"dive"`
}

// A cycle the validator never walks into is not refused: behind
// validate:"-", nostructlevel or structonly, in a collection without a
// dive, or in an unexported field. Nor is a value that reaches one struct
// along several paths without returning to it.
func TestValidation_LoadsWhatTheValidatorNeverWalksInACycle(t *testing.T) {
	looped := func() *cycNode {
		n := &cycNode{Name: "n"}
		n.Next = n
		return n
	}
	_, err := Load[cycUnwalkedCfg](t.Context(), WithPostLoad(func(_ context.Context, c *cycUnwalkedCfg) error {
		c.Skipped, c.NoStruct, c.Only, c.List, c.hidden = looped(), looped(), looped(), []*cycNode{looped()}, looped()
		return nil
	}))
	require.NoError(t, err)

	_, err = Load[cycSharedCfg](t.Context(), WithPostLoad(func(_ context.Context, c *cycSharedCfg) error {
		// A diamond: two paths from top to bottom, every node reached
		// through two fields and both list entries.
		bottom := &cycNode{Name: "bottom"}
		mid := &cycNode{Name: "mid", Next: bottom, Prev: bottom}
		top := &cycNode{Name: "top", Next: mid, Prev: mid, Children: map[string]*cycNode{"a": mid, "b": bottom}}
		c.A, c.B, c.Nodes = top, top, []*cycNode{top, top}
		return nil
	}))
	require.NoError(t, err)
}

type cycPanickyValuer struct{ N int }

func (cycPanickyValuer) ValidatorValue() any { panic("caller valuer") }

type cycPanickyCfg struct {
	Value cycPanickyValuer `koanf:"value"`
}

// A panic raised by the caller's ValidatorValue is the caller's.
func TestValidation_ValuerPanicIsTheCallers(t *testing.T) {
	assert.PanicsWithValue(t, "caller valuer", func() {
		_, _ = Load[cycPanickyCfg](t.Context())
	})
}

// The walk enters each struct once whatever the number of paths to it: a
// chain of diamonds, 2^64 paths deep, takes one step per node.
func TestRefuseCycles_WalksASharedStructOnce(t *testing.T) {
	top := &cycNode{Name: "bottom"}
	for range 64 {
		top = &cycNode{Next: top, Prev: top}
	}
	require.NoError(t, refuseCycles(&cycSharedCfg{A: top}))

	top.Next.Next.Prev = top
	err := refuseCycles(&cycSharedCfg{A: top})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "A.next.next.prev holds a cycle back to A")
}

type cycServer struct {
	Host string `koanf:"host" validate:"required"`
	Port int    `koanf:"port" validate:"min=1"`
}

// cycAcyclicCfg holds what configs hold: no type on the validator's walk
// reaches itself, and no value there is an interface.
type cycAcyclicCfg struct {
	Name     string                `koanf:"name" validate:"required"`
	Server   cycServer             `koanf:"server"`
	TLS      *cycServer            `koanf:"tls"`
	Pools    []cycServer           `koanf:"pools" validate:"dive"`
	Named    map[string]*cycServer `koanf:"named" validate:"dive,keys,min=1,endkeys"`
	Started  time.Time             `koanf:"started"`
	Unwalked *cycNode              `koanf:"-" validate:"-"`
	Extra    map[string]any        `koanf:",remain"`
}

type cycAnyCfg struct {
	Name  string `koanf:"name"`
	Extra any    `koanf:"-"`
}

type cycRemainCfg struct {
	Name  string         `koanf:"name"`
	Extra map[string]any `koanf:",remain" validate:"dive"`
}

type cycValuedCfg struct {
	Valued cycValuer `koanf:"-"`
}

type cycSelfCfg struct {
	Self selfPointer `koanf:"-"`
}

// Whether a config type can hold a cycle on the validator's paths is decided
// from the type: a type the walk reaches again on its path, or a value it
// cannot know the type of, can; a cycle the validator never walks into
// cannot.
func TestMayHoldCycle(t *testing.T) {
	for name, tc := range map[string]struct {
		typ  reflect.Type
		want bool
	}{
		"scalars, structs and dived collections of them": {reflect.TypeFor[*cycAcyclicCfg](), false},
		"a cycle the validator never walks into":         {reflect.TypeFor[*cycUnwalkedCfg](), false},
		"structs linked back to one another":             {reflect.TypeFor[*cycSharedCfg](), true},
		"a pointer of its own type":                      {reflect.TypeFor[*cycSelfCfg](), true},
		"an interface":                                   {reflect.TypeFor[*cycAnyCfg](), true},
		"a dived remain field":                           {reflect.TypeFor[*cycRemainCfg](), true},
		"a validator value":                              {reflect.TypeFor[*cycValuedCfg](), true},
	} {
		assert.Equal(t, tc.want, mayHoldCycle(tc.typ), name)
	}
}

// A config whose type cannot hold a cycle costs validation no walk:
// refuseCycles allocates nothing for it, where a walk allocates its path's
// bookkeeping.
func TestRefuseCycles_SkipsATypeThatCannotHoldOne(t *testing.T) {
	acyclic := &cycAcyclicCfg{Name: "a", TLS: &cycServer{}, Pools: []cycServer{{}}, Named: map[string]*cycServer{"a": {}}}
	assert.Zero(t, testing.AllocsPerRun(10, func() { _ = refuseCycles(acyclic) }))
	walked := &cycSharedCfg{A: &cycNode{Name: "a"}}
	assert.NotZero(t, testing.AllocsPerRun(10, func() { _ = refuseCycles(walked) }), "a type that can hold one is walked")
}

// A config whose only path to a cycle is an interface is walked, and a self
// pointer a hook puts there refused.
func TestValidation_RefusesACycleInAnInterface(t *testing.T) {
	_, err := Load[cycAnyCfg](t.Context(), WithPostLoad(func(_ context.Context, c *cycAnyCfg) error {
		var a any
		a = &a
		c.Extra = a
		return nil
	}))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "Extra holds a cycle back to Extra")
}
