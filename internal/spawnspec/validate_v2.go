package spawnspec

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Validator registration for v2 struct-level rules. Mirrors the v1
// registration in getValidator() but for the v2 type family. Called
// from getValidator (the one-time init).

func registerV2StructValidators(v *validator.Validate) {
	v.RegisterStructValidation(spawnSpecV2StructLevel, SpawnSpecV2{})
}

// spawnSpecV2StructLevel enforces:
//   - the v2 executor-discriminator XOR rule (exactly one of 5 blocks).
//   - the composition table: TmuxBlockV2.Layout == "none" is only
//     valid paired with executor=zmx (per Proposal 0008 / zmx engine
//     docs). A v2 spec with `tmux: { layout: none }` is invalid; the
//     none layout means "no in-pane layout", which only makes sense
//     when the executor itself is the zmx sessions-only engine.
func spawnSpecV2StructLevel(sl validator.StructLevel) {
	s := sl.Current().Interface().(SpawnSpecV2)
	n := 0
	if s.Tmux != nil {
		n++
	}
	if s.CFWorker != nil {
		n++
	}
	if s.CFDurableObject != nil {
		n++
	}
	if s.Cmux != nil {
		n++
	}
	if s.Zmx != nil {
		n++
	}
	switch n {
	case 0:
		sl.ReportError(s, "executor", "Executor", "executor_xor_zero_v2", "")
	case 1:
		// good
	default:
		sl.ReportError(s, "executor", "Executor", "executor_xor_multi_v2", "")
	}

	// Composition-table rule. layout=none on the tmux block is a
	// category error — it's the marker "this engine has no in-pane
	// layout", which is only true for zmx. The validator catches
	// `tmux: { layout: none }` before the engine sees it.
	if s.Tmux != nil && s.Tmux.Layout == "none" {
		sl.ReportError(s, "layout", "Layout", "layout_none_only_with_zmx", "")
	}
}

// ValidateSpecV2 runs struct-tag validation plus v2-specific cross-
// field rules. Used by the version-aware UnmarshalSpec when the YAML
// document declares `spec_version: v2`.
func ValidateSpecV2(s *SpawnSpecV2) error {
	if s == nil {
		return fmt.Errorf("spawnspec: cannot validate nil SpawnSpecV2")
	}
	v, err := getValidator()
	if err != nil {
		return fmt.Errorf("spawnspec: validator init: %w", err)
	}
	if err := v.Struct(s); err != nil {
		return formatErrV2(err)
	}
	for k := range s.Env {
		if !envKey.MatchString(k) {
			return fmt.Errorf("spawnspec: env key %q must match %s", k, envKey)
		}
	}
	return nil
}

// ValidateHandleV2 runs struct-tag validation against a v2
// WorkerHandle. Mirrors ValidateHandle (v1) with one rule
// adjustment: PaneID is mandatory for any of tmux / cmux / zmx
// (engine-native locator pattern, see worker_killer.buildEngineHandle),
// not just tmux.
func ValidateHandleV2(h *WorkerHandleV2) error {
	if h == nil {
		return fmt.Errorf("spawnspec: cannot validate nil WorkerHandleV2")
	}
	v, err := getValidator()
	if err != nil {
		return fmt.Errorf("spawnspec: validator init: %w", err)
	}
	if err := v.Struct(h); err != nil {
		return formatErrV2(err)
	}
	if h.Status == "failed" && strings.TrimSpace(h.Message) == "" {
		return fmt.Errorf("spawnspec: status=failed requires a non-empty message")
	}
	switch h.Executor {
	case "tmux", "cmux", "zmx":
		if h.PaneID == "" {
			return fmt.Errorf("spawnspec: executor=%s requires pane_id (engine-native locator)", h.Executor)
		}
	}
	return nil
}

// formatErrV2 is the v2-side flattener. Extends explain() with the
// v2-only failure tags.
func formatErrV2(err error) error {
	// Delegate to formatErr after augmenting the tag map. Because
	// explain() is a static switch, we wrap its output by post-
	// processing the message for the v2-only tags.
	wrapped := formatErr(err)
	if wrapped == nil {
		return nil
	}
	msg := wrapped.Error()
	msg = strings.ReplaceAll(msg, `failed rule "executor_xor_zero_v2"`,
		"missing executor block (need exactly one of: tmux, cf-worker, cf-durable-object, cmux, zmx)")
	msg = strings.ReplaceAll(msg, `failed rule "executor_xor_multi_v2"`,
		"multiple executor blocks set (exactly one of tmux / cf-worker / cf-durable-object / cmux / zmx allowed)")
	msg = strings.ReplaceAll(msg, `failed rule "layout_none_only_with_zmx"`,
		"layout=none is only valid paired with executor=zmx (composition table; see Proposal 0008)")
	return fmt.Errorf("%s", msg)
}
