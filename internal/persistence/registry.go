package persistence

import (
	"fmt"
	"sort"
)

// Pair is a (persistence-engine, layout-engine) tuple. The registry
// validates these as a closed set — per Proposal 0008's Ousterhout
// review (#4489183265): "make invalid compositions unrepresentable,
// not runtime-checked."
type Pair struct {
	Persistence string
	Layout      string
}

// supportedPairs is the closed composition registry. Editing this is
// the gate for adding a new engine combination — both engines must
// already be registered AND a concrete consumer must exist (no
// speculative entries; see the Ousterhout review on hypothetical
// backends).
//
// Phase A registers only the today-default pair. Phase B will add
// {cmux, cmux}. Cross-engine pairs (tmux+cmux, etc.) require explicit
// forwarder code and stay rejected until that work lands.
var supportedPairs = map[Pair]struct{}{
	{Persistence: "tmux", Layout: "tmux"}: {},
	// Future additions land here, NOT as a free Cartesian product:
	//   {Persistence: "cmux", Layout: "cmux"}: {},   // Phase B
}

// SupportedPairs returns a slice copy of the registered compositions.
// Callers MAY mutate the returned slice; the underlying registry is
// not affected. Order is unspecified.
func SupportedPairs() []Pair {
	out := make([]Pair, 0, len(supportedPairs))
	for p := range supportedPairs {
		out = append(out, p)
	}
	return out
}

// IsSupported reports whether the (persistence, layout) pair is in the
// closed registry.
func IsSupported(persistenceName, layoutName string) bool {
	_, ok := supportedPairs[Pair{Persistence: persistenceName, Layout: layoutName}]
	return ok
}

// RequirePair validates the pair against the closed registry. Returns
// nil when the pair is supported; otherwise returns an error wrapping
// ErrUnsupportedComposition with a diagnostic that lists the supported
// pairs (so operators see what they SHOULD have picked).
//
// Callers should invoke this at flag-parse time so invalid
// compositions fail before any spawn work happens.
func RequirePair(persistenceName, layoutName string) error {
	if IsSupported(persistenceName, layoutName) {
		return nil
	}
	return fmt.Errorf(
		"%w: persistence=%q layout=%q is not in the closed registry; supported: %s",
		ErrUnsupportedComposition, persistenceName, layoutName, formatPairs(SupportedPairs()),
	)
}

// formatPairs renders a Pair slice as a stable, operator-readable list.
// Deterministic order so the diagnostic doesn't flap between invocations.
func formatPairs(pairs []Pair) string {
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Persistence != pairs[j].Persistence {
			return pairs[i].Persistence < pairs[j].Persistence
		}
		return pairs[i].Layout < pairs[j].Layout
	})
	out := ""
	for i, p := range pairs {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("{persistence=%s,layout=%s}", p.Persistence, p.Layout)
	}
	return out
}
