package deepcopy

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func copyOf[T any](v T) T {
	return Value(reflect.ValueOf(v)).Interface().(T)
}

// Sharing and cycles inside the original survive the copy without it
// recursing forever; unexported fields keep their values.
func TestValue_SharingCyclesAndUnexported(t *testing.T) {
	type node struct {
		Next *node
		Tags []string
		note string
	}
	shared := []string{"a"}
	n := &node{Tags: shared, note: "kept"}
	n.Next = n
	self := map[string]any{"tags": shared}
	self["self"] = self

	out := copyOf(map[string]any{"node": n, "self": self, "again": shared})

	cn := out["node"].(*node)
	assert.NotSame(t, n, cn)
	assert.Same(t, cn, cn.Next, "the cycle points at the copy")
	assert.Equal(t, "kept", cn.note)
	cself := out["self"].(map[string]any)
	assert.Equal(t, reflect.ValueOf(cself).Pointer(), reflect.ValueOf(cself["self"]).Pointer())

	shared[0] = "changed"
	assert.Equal(t, []string{"a"}, cn.Tags)
	assert.Equal(t, []string{"a"}, out["again"])
}

type inner struct {
	Hosts  []string
	Labels map[string]string
	Peer   *peer
	deeper
	secret []byte
}

type deeper struct {
	Ports []int
}

type peer struct{ Name string }

type Exported struct {
	Tags []string
}

type hidden struct {
	Tags []string
}

type promoting struct {
	inner
	*Exported
	*hidden
	Direct []string
	When   time.Time
	opaque map[string]int
}

// What an unexported embedded struct promotes — at any depth — is copied
// like a direct exported field, while its own unexported state, a type
// built from such state, and an unexported embedded pointer, which the copy
// cannot replace, keep the original's value.
func TestValue_DetachesWhatUnexportedEmbeddingsPromote(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	orig := promoting{
		inner: inner{
			Hosts:  []string{"a"},
			Labels: map[string]string{"k": "v"},
			Peer:   &peer{Name: "p"},
			deeper: deeper{Ports: []int{1}},
			secret: []byte("s"),
		},
		Exported: &Exported{Tags: []string{"t"}},
		hidden:   &hidden{Tags: []string{"h"}},
		Direct:   []string{"d"},
		When:     when,
		opaque:   map[string]int{"o": 1},
	}
	got := copyOf(orig)

	orig.Hosts[0] = "changed"
	orig.Labels["k"] = "changed"
	orig.Peer.Name = "changed"
	orig.Ports[0] = 9
	orig.Exported.Tags[0] = "changed"
	orig.Direct[0] = "changed"

	assert.Equal(t, []string{"a"}, got.Hosts)
	assert.Equal(t, map[string]string{"k": "v"}, got.Labels)
	assert.Equal(t, "p", got.Peer.Name)
	assert.Equal(t, []int{1}, got.Ports)
	assert.Equal(t, []string{"t"}, got.Exported.Tags)
	assert.Equal(t, []string{"d"}, got.Direct)
	assert.True(t, when.Equal(got.When))
	assert.Equal(t, when.Location(), got.When.Location())

	orig.secret[0] = 'x'
	orig.opaque["o"] = 2
	orig.hidden.Tags[0] = "changed"
	assert.Equal(t, []byte("x"), got.secret, "unexported state is opaque, shared as it was")
	assert.Equal(t, 2, got.opaque["o"])
	assert.Same(t, orig.hidden, got.hidden)
}

// A value shared between a promoted field and a direct one stays shared
// inside the copy.
func TestValue_SharingAcrossPromotion(t *testing.T) {
	type holder struct {
		inner
		Again []string
	}
	hosts := []string{"a"}
	got := copyOf(holder{inner: inner{Hosts: hosts}, Again: hosts})
	got.Hosts[0] = "b"
	assert.Equal(t, "b", got.Again[0])
	assert.Equal(t, "a", hosts[0])
}

// A copy of an addressable value shares nothing with it, a scalar's or a
// nil's included: setting the original leaves the copy as it was, and
// setting the copy leaves the original.
func TestValue_AddressableValuesAreDetached(t *testing.T) {
	type section struct {
		N    int
		Name string
		P    *int
		M    map[string]int
		F    func() int
	}
	var s section
	v := reflect.ValueOf(&s).Elem()
	copies := make([]reflect.Value, v.NumField())
	for i := range copies {
		copies[i] = Value(v.Field(i))
	}
	one := 1
	s = section{N: 1, Name: "x", P: &one, M: map[string]int{"a": 1}, F: func() int { return 1 }}
	for i, c := range copies {
		assert.True(t, c.IsZero(), "field %s copied before the original changed", v.Type().Field(i).Name)
		if c.CanSet() {
			c.Set(reflect.Zero(c.Type()))
		}
	}
	assert.Equal(t, 1, s.N)
	assert.Equal(t, "x", s.Name)
}
