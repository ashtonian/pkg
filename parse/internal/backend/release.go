package backend

// The release of parse this contract is part of. parse and its provider
// modules release together at one version, and release-please writes it
// here as it releases them. Each provider module holds its own release
// beside these (its contract.go) and compares them at compile time, so a
// build that pairs a provider with another release of parse fails there,
// naming both releases.

const ReleaseMajor = 0 // x-release-please-major

const ReleaseMinor = 18 // x-release-please-minor

const ReleasePatch = 0 // x-release-please-patch
