package spawnspec

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
)

var (
	validateOnce sync.Once
	validatorV   *validator.Validate
	validatorErr error
)

// dnsLabel matches the same shape as a DNS-1123 label, slightly
// loosened for length (we allow up to 63 chars, the DNS limit). Used
// for SpawnSpec.Name so the value round-trips cleanly through NATS
// subjects, tmux pane titles, and filesystem paths.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// envKey matches POSIX-shell-safe env var names — what the shell will
// happily expand from `${FOO}`. Defensive: most operators expect this.
var envKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func getValidator() (*validator.Validate, error) {
	validateOnce.Do(func() {
		v := validator.New(validator.WithRequiredStructEnabled())

		if err := v.RegisterValidation("dns_label", func(fl validator.FieldLevel) bool {
			s := fl.Field().String()
			return len(s) > 0 && len(s) <= 63 && dnsLabel.MatchString(s)
		}); err != nil {
			validatorErr = err
			return
		}

		if err := v.RegisterValidation("agent", func(fl validator.FieldLevel) bool {
			got := Agent(fl.Field().String())
			return slices.Contains(KnownAgents(), got)
		}); err != nil {
			validatorErr = err
			return
		}

		v.RegisterStructValidation(spawnSpecStructLevel, SpawnSpec{})
		v.RegisterStructValidation(outfitStructLevel, OutfitBlock{})
		registerV2StructValidators(v)

		validatorV = v
	})
	return validatorV, validatorErr
}

// ValidateSpec runs struct-tag validation plus the cross-field rules
// (XOR executor, outfit-shape XOR, env-key shape). Returns nil on
// success or a flattened multi-error on failure.
func ValidateSpec(s *SpawnSpec) error {
	if s == nil {
		return fmt.Errorf("spawnspec: cannot validate nil SpawnSpec")
	}
	v, err := getValidator()
	if err != nil {
		return fmt.Errorf("spawnspec: validator init: %w", err)
	}
	if err := v.Struct(s); err != nil {
		return formatErr(err)
	}
	// Env-var key shape check (validator can't reach inside map keys
	// without a custom rule, and a one-off check is clearer than
	// teaching the library a `dive,keys` rule).
	for k := range s.Env {
		if !envKey.MatchString(k) {
			return fmt.Errorf("spawnspec: env key %q must match %s", k, envKey)
		}
	}
	return nil
}

// ValidateHandle runs struct-tag validation against a WorkerHandle.
// Cross-field rules: Status=failed implies Message != "", Executor=tmux
// implies PaneID != "" (and ID empty).
func ValidateHandle(h *WorkerHandle) error {
	if h == nil {
		return fmt.Errorf("spawnspec: cannot validate nil WorkerHandle")
	}
	v, err := getValidator()
	if err != nil {
		return fmt.Errorf("spawnspec: validator init: %w", err)
	}
	if err := v.Struct(h); err != nil {
		return formatErr(err)
	}
	if h.Status == "failed" && strings.TrimSpace(h.Message) == "" {
		return fmt.Errorf("spawnspec: status=failed requires a non-empty message")
	}
	if h.Executor == "tmux" && h.PaneID == "" {
		return fmt.Errorf("spawnspec: executor=tmux requires pane_id")
	}
	return nil
}

// spawnSpecStructLevel enforces the executor-discriminator XOR rule:
// exactly one of Tmux / CFWorker / CFDurableObject must be set.
//
// Validator's struct-level callbacks let us report the rule by name
// ("executor_xor"), which surfaces well in error formatting.
func spawnSpecStructLevel(sl validator.StructLevel) {
	s := sl.Current().Interface().(SpawnSpec)
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
	switch n {
	case 0:
		sl.ReportError(s, "executor", "Executor", "executor_xor_zero", "")
	case 1:
		// good
	default:
		sl.ReportError(s, "executor", "Executor", "executor_xor_multi", "")
	}
}

// outfitStructLevel enforces the outfit-shape XOR rule: either Bundle
// shorthand OR explicit (Name+Cut+optional Accessories), not both.
func outfitStructLevel(sl validator.StructLevel) {
	o := sl.Current().Interface().(OutfitBlock)
	hasBundle := o.Bundle != ""
	hasExplicit := o.Name != "" || o.Cut != "" || len(o.Accessories) > 0
	if hasBundle && hasExplicit {
		sl.ReportError(o, "outfit", "Outfit", "outfit_xor", "")
	}
	if !hasBundle && !hasExplicit {
		sl.ReportError(o, "outfit", "Outfit", "outfit_empty", "")
	}
}

// formatErr turns the noisy default validator output into a few short
// lines, one per failed field. Each line names the field and the rule
// it tripped — enough for the operator to find and fix.
func formatErr(err error) error {
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return err
	}
	parts := make([]string, 0, len(ve))
	for _, fe := range ve {
		parts = append(parts, fmt.Sprintf("  - %s: %s", fe.Namespace(), explain(fe)))
	}
	return fmt.Errorf("spawnspec: validation failed:\n%s", strings.Join(parts, "\n"))
}

// explain maps a validator tag to a human-readable cause. Keeps the
// failure messages in one place rather than scattered across types.
func explain(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "required"
	case "dns_label":
		return fmt.Sprintf("must be DNS-label-shaped (lowercase, hyphens, no dots); got %q", fe.Value())
	case "agent":
		return fmt.Sprintf("must be one of %v; got %q", KnownAgents(), fe.Value())
	case "oneof":
		return fmt.Sprintf("must be one of [%s]; got %q", fe.Param(), fe.Value())
	case "executor_xor_zero":
		return "missing executor block (need exactly one of: tmux, cf-worker, cf-durable-object)"
	case "executor_xor_multi":
		return "multiple executor blocks set (exactly one allowed)"
	case "outfit_xor":
		return "set either bundle shorthand OR explicit name/cut/accessories, not both"
	case "outfit_empty":
		return "set either bundle shorthand OR explicit name/cut/accessories"
	default:
		return fmt.Sprintf("failed rule %q", fe.Tag())
	}
}

