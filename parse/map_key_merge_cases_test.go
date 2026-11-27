//go:build unit || integration

package parse_test

// keyedMapRefusals name a member no key of the map's type decodes from, or
// two members that decode to one key.
func keyedMapRefusals() []struct{ name, body string } {
	cases := []struct{ name, body string }{
		{"not an integer", `{"by_int":{"x":1}}`},
		{"an integer in exponent form", `{"by_int":{"1e2":1}}`},
		{"past the unsigned range", `{"by_uint":{"256":"x"}}`},
		{"a negative unsigned key", `{"by_uint":{"-1":"x"}}`},
		{"not an address", `{"by_addr":{"host.example":1}}`},
		{"two spellings of one integer key", `{"by_int":{"01":1,"1":2}}`},
		{"two spellings of one address", `{"by_addr":{"::1":1,"0:0:0:0:0:0:0:1":2}}`},
	}
	return cases
}
