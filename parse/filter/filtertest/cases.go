package filtertest

import "strings"

func ids(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "")
}

// Cases is the operator × type matrix over Records. Every predicate but the
// NULL tests is NULL on a NULL field, so negation never matches row d.
func Cases() []Case {
	return []Case{
		// --- string: case-sensitive equality, code-point ordering ---
		{Name: "string eq", Filter: `{"name":"beta"}`, Want: ids("b")},
		{Name: "string eq is case-sensitive", Filter: `{"name":"BETA"}`, Want: ids("")},
		{Name: "string ne excludes null", Filter: `{"name":{"$ne":"beta"}}`, Want: ids("acefgh")},
		{Name: "string gt code point", Filter: `{"name":{"$gt":"Z"}}`, Want: ids("befgh")},
		{Name: "string gte", Filter: `{"name":{"$gte":"beta"}}`, Want: ids("bfg")},
		{Name: "string lt", Filter: `{"name":{"$lt":"a"}}`, Want: ids("ac")},
		{Name: "string lte", Filter: `{"name":{"$lte":"Alpha"}}`, Want: ids("a")},
		{Name: "string in", Filter: `{"name":{"$in":["beta","del_ta","nope"]}}`, Want: ids("bf")},
		{Name: "string nin excludes null", Filter: `{"name":{"$nin":["beta"]}}`, Want: ids("acefgh")},

		// --- string: LIKE patterns, % _ and \ escapes ---
		{Name: "like is case-sensitive", Filter: `{"name":{"$like":"alpha%"}}`, Want: ids("e")},
		{Name: "like suffix", Filter: `{"name":{"$like":"%a"}}`, Want: ids("abefg")},
		{Name: "like underscore is one character", Filter: `{"name":{"$like":"_eta"}}`, Want: ids("b")},
		{Name: "like escaped underscore", Filter: `{"name":{"$like":"del\\_ta"}}`, Want: ids("f")},
		{Name: "like underscore wildcard", Filter: `{"name":{"$like":"d_l%"}}`, Want: ids("f")},
		{Name: "like escaped percent", Filter: `{"name":{"$like":"%\\%"}}`, Want: ids("c")},
		{Name: "like escaped backslash", Filter: `{"name":{"$like":"back\\\\%"}}`, Want: ids("h")},
		{Name: "like glob metacharacters are literal", Filter: `{"name":{"$like":"*"}}`, Want: ids("")},
		{Name: "like trailing escape rejected", Filter: `{"name":{"$like":"abc\\"}}`, ParseErr: "E2073"},
		{Name: "ilike folds case", Filter: `{"name":{"$ilike":"alpha%"}}`, Want: ids("ae")},
		{Name: "ilike with escape", Filter: `{"name":{"$ilike":"%GAMMA\\%"}}`, Want: ids("c")},
		{Name: "ilike prefix", Filter: `{"name":{"$ilike":"B%"}}`, Want: ids("bh")},

		// --- string: literal substring ---
		{Name: "contains is literal", Filter: `{"name":{"$contains":"a%"}}`, Want: ids("c")},
		{Name: "contains underscore is literal", Filter: `{"name":{"$contains":"_"}}`, Want: ids("f")},
		{Name: "contains", Filter: `{"name":{"$contains":"ph"}}`, Want: ids("ae")},
		{Name: "contains is case-sensitive", Filter: `{"name":{"$contains":"PH"}}`, Want: ids("")},
		{Name: "contains backslash", Filter: `{"name":{"$contains":"\\"}}`, Want: ids("h")},
		{Name: "contains empty matches every value", Filter: `{"name":{"$contains":""}}`, Want: ids("abcefgh")},

		// --- NULL tests ---
		{Name: "null true", Filter: `{"name":{"$null":true}}`, Want: ids("d")},
		{Name: "null false", Filter: `{"name":{"$null":false}}`, Want: ids("abcefgh")},
		{Name: "exists true", Filter: `{"count":{"$exists":true}}`, Want: ids("abcefgh")},
		{Name: "exists false", Filter: `{"active":{"$exists":false}}`, Want: ids("dg")},

		// --- int: exact beyond 2^53 ---
		{Name: "int eq above 2^53", Filter: `{"count":9007199254740993}`, Want: ids("e")},
		{Name: "int eq", Filter: `{"count":1}`, Want: ids("a")},
		{Name: "int eq integral float", Filter: `{"count":1.0}`, Want: ids("a")},
		{Name: "int ne", Filter: `{"count":{"$ne":1}}`, Want: ids("bcefgh")},
		{Name: "int gt above 2^53", Filter: `{"count":{"$gt":9007199254740992}}`, Want: ids("e")},
		{Name: "int gte above 2^53", Filter: `{"count":{"$gte":9007199254740992}}`, Want: ids("eh")},
		{Name: "int lt", Filter: `{"count":{"$lt":0}}`, Want: ids("g")},
		{Name: "int lte", Filter: `{"count":{"$lte":1}}`, Want: ids("afg")},
		{Name: "int in", Filter: `{"count":{"$in":[1,3,9007199254740992]}}`, Want: ids("ach")},
		{Name: "int nin", Filter: `{"count":{"$nin":[1,2]}}`, Want: ids("cefgh")},
		{Name: "int in empty", Filter: `{"count":{"$in":[]}}`, Want: ids("")},
		{Name: "int nin empty matches null too", Filter: `{"count":{"$nin":[]}}`, Want: ids("abcdefgh")},
		{Name: "int fraction rejected", Filter: `{"count":1.5}`, ParseErr: "E2073"},

		// --- float ---
		{Name: "float eq", Filter: `{"score":2.5}`, Want: ids("bh")},
		{Name: "float eq integer literal", Filter: `{"score":2}`, Want: ids("")},
		{Name: "float ne", Filter: `{"score":{"$ne":2.5}}`, Want: ids("acefg")},
		{Name: "float gt", Filter: `{"score":{"$gt":1.5}}`, Want: ids("bgh")},
		{Name: "float gte", Filter: `{"score":{"$gte":0.001}}`, Want: ids("abfgh")},
		{Name: "float lt", Filter: `{"score":{"$lt":0.001}}`, Want: ids("ce")},
		{Name: "float lte", Filter: `{"score":{"$lte":0}}`, Want: ids("ce")},
		{Name: "float in", Filter: `{"score":{"$in":[0,100.25]}}`, Want: ids("eg")},
		{Name: "float nin", Filter: `{"score":{"$nin":[2.5]}}`, Want: ids("acefg")},

		// --- bool ---
		{Name: "bool eq true", Filter: `{"active":true}`, Want: ids("acfh")},
		{Name: "bool eq false", Filter: `{"active":false}`, Want: ids("be")},
		{Name: "bool ne excludes null", Filter: `{"active":{"$ne":true}}`, Want: ids("be")},
		{Name: "bool in", Filter: `{"active":{"$in":[false]}}`, Want: ids("be")},
		{Name: "bool nin", Filter: `{"active":{"$nin":[true]}}`, Want: ids("be")},

		// --- timestamp ---
		{Name: "time eq", Filter: `{"at":"2026-01-02T03:04:07Z"}`, Want: ids("b")},
		{Name: "time eq other offset", Filter: `{"at":"2026-01-02T04:04:07+01:00"}`, Want: ids("b")},
		{Name: "time ne", Filter: `{"at":{"$ne":"2026-01-02T03:04:06Z"}}`, Want: ids("bcefg")},
		{Name: "time gt", Filter: `{"at":{"$gt":"2026-01-02T03:04:08Z"}}`, Want: ids("efg")},
		{Name: "time gte", Filter: `{"at":{"$gte":"2026-01-02T03:04:08Z"}}`, Want: ids("cefg")},
		{Name: "time lt", Filter: `{"at":{"$lt":"2026-01-02T03:04:07Z"}}`, Want: ids("a")},
		{Name: "time lte", Filter: `{"at":{"$lte":"2026-01-02T03:04:07Z"}}`, Want: ids("ab")},
		{Name: "time in", Filter: `{"at":{"$in":["2026-01-02T03:04:06Z","2026-01-02T03:04:11Z"]}}`, Want: ids("af")},
		{Name: "time nin", Filter: `{"at":{"$nin":["2026-01-02T03:04:06Z"]}}`, Want: ids("bcefg")},
		{Name: "time relative token", Filter: `{"at":{"$lt":"now+1s"}}`, Want: ids("abc")},
		{Name: "time relative token in set", Filter: `{"at":{"$in":["now-1s"]}}`, Want: ids("b")},
		{Name: "time null", Filter: `{"at":{"$null":true}}`, Want: ids("dh")},

		// --- uuid: canonical form ---
		{Name: "uuid eq uppercase input", Filter: `{"ref":"0198A3C2-7A1E-7C00-8000-000000000002"}`, Want: ids("b")},
		{Name: "uuid ne", Filter: `{"ref":{"$ne":"0198a3c2-7a1e-7c00-8000-000000000001"}}`, Want: ids("bcefg")},
		{Name: "uuid in", Filter: `{"ref":{"$in":["0198a3c2-7a1e-7c00-8000-000000000001","0198a3c2-7a1e-7c00-8000-000000000007"]}}`, Want: ids("ag")},
		{Name: "uuid nin", Filter: `{"ref":{"$nin":["0198a3c2-7a1e-7c00-8000-000000000001"]}}`, Want: ids("bcefg")},
		{Name: "uuid malformed rejected", Filter: `{"ref":"not-a-uuid"}`, ParseErr: "E2116"},

		// --- JSON documents: jsonb equality ---
		{Name: "json eq ignores key order and 1 vs 1.0", Filter: `{"doc":{"env":"prod","n":1}}`, Want: ids("g")},
		{Name: "json eq nested", Filter: `{"doc":{"$eq":{"n":1,"env":"prod","tags":["x","y"]}}}`, Want: ids("a")},
		{Name: "json eq array order matters", Filter: `{"doc":{"$eq":{"env":"prod","n":1,"tags":["y","x"]}}}`, Want: ids("")},
		{Name: "json eq array", Filter: `{"doc":{"$eq":[1,2,3]}}`, Want: ids("e")},
		{Name: "json eq array reordered", Filter: `{"doc":{"$eq":[3,2,1]}}`, Want: ids("")},
		{Name: "json eq scalar document", Filter: `{"doc":{"$eq":"text"}}`, Want: ids("f")},
		{Name: "json eq empty object", Filter: `{"doc":{"$eq":{}}}`, Want: ids("h")},
		{Name: "json ne excludes null", Filter: `{"doc":{"$ne":{"env":"dev","n":2}}}`, Want: ids("acefgh")},
		{Name: "json eq null rejected", Filter: `{"doc":{"$eq":null}}`, ParseErr: "E2073"},

		// --- JSON documents: jsonb containment ---
		{Name: "json contains member", Filter: `{"doc":{"$contains":{"env":"prod"}}}`, Want: ids("acg")},
		{Name: "json contains 1 matches 1.0", Filter: `{"doc":{"$contains":{"n":1}}}`, Want: ids("ag")},
		{Name: "json contains other number", Filter: `{"doc":{"$contains":{"n":1.5}}}`, Want: ids("")},
		{Name: "json contains nested", Filter: `{"doc":{"$contains":{"nested":{"a":[2]}}}}`, Want: ids("c")},
		{Name: "json contains array member", Filter: `{"doc":{"$contains":{"tags":["y"]}}}`, Want: ids("a")},
		{Name: "json nested scalar is not an array member", Filter: `{"doc":{"$contains":{"tags":"y"}}}`, Want: ids("")},
		{Name: "json contains array elements", Filter: `{"doc":{"$contains":[2]}}`, Want: ids("e")},
		{Name: "json top-level array contains scalar", Filter: `{"doc":{"$contains":2}}`, Want: ids("e")},
		{Name: "json scalar contains itself", Filter: `{"doc":{"$contains":"text"}}`, Want: ids("f")},
		{Name: "json empty object contained by every object", Filter: `{"doc":{"$contains":{}}}`, Want: ids("abcgh")},
		{Name: "json empty array contained by every array", Filter: `{"doc":{"$contains":[]}}`, Want: ids("e")},
		{Name: "json null", Filter: `{"doc":{"$null":true}}`, Want: ids("d")},

		// --- arrays: element containment ---
		{Name: "array contains element", Filter: `{"tags":{"$contains":["x"]}}`, Want: ids("acfg")},
		{Name: "array contains scalar shorthand", Filter: `{"tags":{"$contains":"x"}}`, Want: ids("acfg")},
		{Name: "array contains all elements", Filter: `{"tags":{"$contains":["x","y"]}}`, Want: ids("a")},
		{Name: "array contains ignores order", Filter: `{"tags":{"$contains":["z","y"]}}`, Want: ids("h")},
		{Name: "array contains ignores multiplicity", Filter: `{"tags":{"$contains":["x","x"]}}`, Want: ids("acfg")},
		{Name: "array contains empty set", Filter: `{"tags":{"$contains":[]}}`, Want: ids("abcefgh")},
		{Name: "array null", Filter: `{"tags":{"$null":true}}`, Want: ids("d")},
		{Name: "array eq not defined", Filter: `{"tags":["x"]}`, ParseErr: "E2072"},

		// --- geo ---
		{Name: "near", Filter: `{"loc":{"$near":{"lat":10,"lon":10,"distance":"50km"}}}`, Want: ids("a"), Unsupported: []string{"memory"}},
		{Name: "near radius", Filter: `{"loc":{"$near":{"lat":20,"lon":20,"distance":"100km"}}}`, Want: ids("bf"), Unsupported: []string{"memory"}},
		{Name: "near at the center point", Filter: `{"loc":{"$near":{"lat":0,"lon":0,"distance":"1m"}}}`, Want: ids("e"), Unsupported: []string{"memory"}},
		{Name: "within box includes corner", Filter: `{"loc":{"$within":{"top_left":{"lat":25,"lon":0},"bottom_right":{"lat":5,"lon":25}}}}`, Want: ids("abfh"), Unsupported: []string{"memory"}},
		{Name: "within box includes edge", Filter: `{"loc":{"$within":{"top_left":{"lat":20,"lon":19},"bottom_right":{"lat":0,"lon":21}}}}`, Want: ids("b"), Unsupported: []string{"memory"}},
		{Name: "within polygon", Filter: `{"loc":{"$within":{"points":[{"lat":0,"lon":0},{"lat":30,"lon":0},{"lat":0,"lon":30}]}}}`, Want: ids("ae"), Unsupported: []string{"memory", "sqlite"}},
		{Name: "intersects box", Filter: `{"loc":{"$intersects":{"top_left":{"lat":25,"lon":0},"bottom_right":{"lat":5,"lon":25}}}}`, Want: ids("abfh"), Unsupported: []string{"memory", "sqlite"}},
		{Name: "within inverted box rejected", Filter: `{"loc":{"$within":{"top_left":{"lat":25,"lon":25},"bottom_right":{"lat":5,"lon":0}}}}`, ParseErr: "E2112"},
		{Name: "geo null", Filter: `{"loc":{"$null":true}}`, Want: ids("dg")},

		// --- three-valued logic ---
		{Name: "not eq excludes null", Filter: `{"$not":{"count":1}}`, Want: ids("bcefgh")},
		{Name: "not like excludes null", Filter: `{"$not":{"name":{"$like":"%a"}}}`, Want: ids("ch")},
		{Name: "or with null test", Filter: `{"$or":[{"count":1},{"name":{"$null":true}}]}`, Want: ids("ad")},
		{Name: "not or with unknown", Filter: `{"$not":{"$or":[{"count":1},{"active":true}]}}`, Want: ids("be")},
		{Name: "not json contains excludes null", Filter: `{"$not":{"doc":{"$contains":{"env":"prod"}}}}`, Want: ids("befh")},
		{Name: "not array contains excludes null", Filter: `{"$not":{"tags":{"$contains":["x"]}}}`, Want: ids("beh")},
		{Name: "not empty in matches null too", Filter: `{"$not":{"count":{"$in":[]}}}`, Want: ids("abcdefgh")},
		{Name: "and", Filter: `{"$and":[{"active":true},{"score":{"$gt":1}}]}`, Want: ids("ah")},
	}
}
