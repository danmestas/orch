package persistence

import "errors"

// ErrNotFound is returned by Engine.Attach when no worker with the
// requested slug is registered. Callers MUST use errors.Is to detect
// this; engines wrap it with engine-specific context.
var ErrNotFound = errors.New("persistence: worker not found")

// ErrUnsupportedComposition is returned by RequirePair when the
// (persistence, layout) pair is not in the closed registry.
var ErrUnsupportedComposition = errors.New("persistence: unsupported composition")
