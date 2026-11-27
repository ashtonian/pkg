package parse

import (
	"fmt"

	"github.com/ashtonian/pkg/parse/patch"
)

// ParsePatch validates and parses a JSON Patch document.
func (r *resourceCore) ParsePatch(body []byte, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.NewParser(r.patchSchema, r.cfg).Parse(body)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ParseMergePatch validates and parses a JSON Merge Patch (RFC 7396) body.
func (r *resourceCore) ParseMergePatch(body []byte, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.ParseMergePatch(body, r.patchSchema, r.cfg)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// MergePatchMap validates a JSON Merge Patch given as a map — decoded
// elsewhere, or built in code — exactly as ParseMergePatch validates its
// body: a nested map merges, any other value (a Go value of the path's type
// included) replaces, nil removes. No JSON round trip.
func (r *resourceCore) MergePatchMap(m map[string]any, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.ParseMergePatchMap(m, r.patchSchema, r.cfg)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ServerMergePatchMap is MergePatchMap for a write the server makes, which
// may also set the `parse:"server"` fields (ParseServerMergePatch).
func (r *resourceCore) ServerMergePatchMap(m map[string]any) (*patch.ParsedPatch, error) {
	return patch.ParseMergePatchMap(m, r.serverPatchSchema, r.cfg)
}

// ValidateOperations validates JSON Patch operations built in code against
// the patch schema, as ParsePatch validates a body: paths, operations,
// value types, limits. Use it instead of assembling a ParsedPatch by hand.
func (r *resourceCore) ValidateOperations(ops []patch.RawPatchOp, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.NewParser(r.patchSchema, r.cfg).ParseOps(ops)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ValidateServerOperations is ValidateOperations for a write the server
// makes, against the server patch schema.
func (r *resourceCore) ValidateServerOperations(ops []patch.RawPatchOp) (*patch.ParsedPatch, error) {
	return patch.NewParser(r.serverPatchSchema, r.cfg).ParseOps(ops)
}

func applyPatchOptions(parsed *patch.ParsedPatch, opts []PatchOption) (*patch.ParsedPatch, error) {
	var cfg patchOptConfig
	for _, o := range opts {
		o(&cfg)
	}
	err := patch.ApplyPatchPolicyConstraints(parsed, cfg.policy, cfg.subject)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// ParseServerMergePatch parses a JSON Merge Patch the server itself writes:
// it may also set the `parse:"server"` fields — columns only the server
// sets, such as a rotated credential's fingerprint. Never pass it a request
// body; ParseMergePatch is the request path. The result applies through the
// same repositories, ApplyValidatedPatch included.
func (r *resourceCore) ParseServerMergePatch(body []byte) (*patch.ParsedPatch, error) {
	return patch.ParseMergePatch(body, r.serverPatchSchema, r.cfg)
}

// ApplyValidatedPatch applies an already-parsed patch to target and returns
// the patched value, never mutating target (patch.Applier). A patch this
// resource did not parse, by hand or against another schema, is ErrForeign.
func (r *Resource[T]) ApplyValidatedPatch(parsed *patch.ParsedPatch, target T) (T, error) {
	err := r.ownsPatch(parsed, "patch")
	if err != nil {
		var zero T
		return zero, err
	}
	return r.applier.Apply(parsed, target)
}

// ownsPatch refuses a patch neither of the resource's patch schemas
// validated. A WithPaths view shares its base's patch schemas, so each
// accepts the other's patches.
func (r *resourceCore) ownsPatch(p *patch.ParsedPatch, role string) error {
	if p.ParsedFor(r.patchSchema) || p.ParsedFor(r.serverPatchSchema) {
		return nil
	}
	return fmt.Errorf("parse: %s: %s: %w", r.table, role, ErrForeign)
}

// ApplyPatch validates a JSON Patch document and applies it to a struct.
func (r *Resource[T]) ApplyPatch(body []byte, target T, opts ...PatchOption) (T, error) {
	var zero T
	parsed, err := r.ParsePatch(body, opts...)
	if err != nil {
		return zero, err
	}
	return r.ApplyValidatedPatch(parsed, target)
}

// PatchOption configures optional behavior for patch parsing.
type PatchOption func(*patchOptConfig)

type patchOptConfig struct {
	policy  *patch.PatchPolicyConstraints
	subject *patch.PolicySubject
}

// WithPatchPolicy sets policy constraints for patch validation.
func WithPatchPolicy(pc *patch.PatchPolicyConstraints, subject *patch.PolicySubject) PatchOption {
	return func(c *patchOptConfig) { c.policy = pc; c.subject = subject }
}
