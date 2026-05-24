package persistence

import "errors"

// ErrUnsupportedComposition is returned by RequirePair when the
// (persistence, layout) pair is not in the closed registry.
var ErrUnsupportedComposition = errors.New("persistence: unsupported composition")
