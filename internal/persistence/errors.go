package persistence

import "errors"

// ErrUnsupportedComposition is returned by RequirePair when the
// (persistence, layout) pair is not in the closed registry.
var ErrUnsupportedComposition = errors.New("persistence: unsupported composition")

// ErrNotImplemented signals that an Engine method has no implementation
// yet for the current engine. Returned by Attach/List on engines whose
// Phase 1 brief didn't include those operations (no caller yet).
var ErrNotImplemented = errors.New("persistence: not implemented")
