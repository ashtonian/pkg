package parsepgx

import "github.com/ashtonian/pkg/parse/internal/backend"

// This module's release, which release-please writes as it releases parse
// and its provider modules together at one version.

const releaseMajor = 0 // x-release-please-major

const releaseMinor = 19 // x-release-please-minor

const releasePatch = 0 // x-release-please-patch

// This module runs on parse's internal contract, which changes between
// releases, so a build that pairs it with another release of parse fails
// here, naming this module's release and then parse's as arrays:
//
//	cannot use … (value of type [0][18][0]struct{}) as [0][19][0]struct{} value
//
// is this module at 0.18.0 on parse 0.19.0. Require one release of both.
var _ [backend.ReleaseMajor][backend.ReleaseMinor][backend.ReleasePatch]struct{} = [releaseMajor][releaseMinor][releasePatch]struct{}{}
