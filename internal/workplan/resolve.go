package workplan

import (
	"fmt"
	"strings"

	"github.com/nitinshyamk/nm/internal/config"
)

// Resolve finds the one workplan a query names.
//
// A workplan has no hash, so unlike a task there is nothing to disambiguate: the
// name is the directory. A prefix is still accepted, because completion offers
// full names and a typed prefix should reach the same place.
func Resolve(cfg config.Config, query string) (Workplan, error) {
	plans, err := List(cfg)
	if err != nil {
		return Workplan{}, err
	}
	return resolveAmong(plans, query, cfg.Workplans())
}

func resolveAmong(plans []Workplan, query, root string) (Workplan, error) {
	if len(plans) == 0 {
		return Workplan{}, fmt.Errorf("no workplans in %s", root)
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return Workplan{}, fmt.Errorf("which workplan? pass a name")
	}

	// An exact match wins outright, so a workplan whose name is a prefix of
	// another is still reachable by typing it in full.
	for _, p := range plans {
		if strings.EqualFold(p.Name, needle) {
			return p, nil
		}
	}

	var byPrefix []Workplan
	for _, p := range plans {
		if strings.HasPrefix(strings.ToLower(p.Name), needle) {
			byPrefix = append(byPrefix, p)
		}
	}
	switch len(byPrefix) {
	case 1:
		return byPrefix[0], nil
	case 0:
		return Workplan{}, fmt.Errorf("no workplan matching %q in %s", query, root)
	default:
		names := make([]string, 0, len(byPrefix))
		for _, p := range byPrefix {
			names = append(names, p.Name)
		}
		return Workplan{}, fmt.Errorf("%q matches %d workplans: %s",
			query, len(byPrefix), strings.Join(names, ", "))
	}
}

// Names lists every workplan name starting with prefix, for shell completion. A
// workplans root that cannot be read yields no suggestions rather than an error,
// because completion runs on every tab press and must stay silent.
func Names(cfg config.Config, prefix string) []string {
	plans, err := List(cfg)
	if err != nil {
		return nil
	}
	needle := strings.ToLower(prefix)
	out := make([]string, 0, len(plans))
	for _, p := range plans {
		if strings.HasPrefix(strings.ToLower(p.Name), needle) {
			out = append(out, p.Name)
		}
	}
	return out
}

// Coerce turns a name a human typed into one ValidateName accepts, for the
// skill that asks an author for a workplan name and should not bounce it back
// over a capital letter or a space.
//
// It only removes ambiguity it can remove safely: case, separators, and
// surrounding punctuation. A name with nothing usable left in it is an error
// rather than a silently invented one, because the workplan directory is where
// the author will look for their work and it should be named what they meant.
func Coerce(name string) (string, error) {
	var b strings.Builder
	lastWasDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasDash = false
		case r == '.', r == '_':
			b.WriteRune(r)
			lastWasDash = false
		case r == ' ', r == '-', r == '/', r == '\\', r == '\t':
			// Runs of separators collapse, so "my  plan" and "my-plan" agree.
			if !lastWasDash && b.Len() > 0 {
				b.WriteRune('-')
				lastWasDash = true
			}
		default:
			// Anything else — quotes, colons, emoji — is dropped rather than
			// transliterated.
		}
	}
	coerced := strings.Trim(b.String(), "-._")
	if coerced == "" {
		return "", fmt.Errorf("%q has nothing in it that can name a workplan", name)
	}
	if err := ValidateName(coerced); err != nil {
		return "", fmt.Errorf("%q becomes %q, which is still not usable: %w", name, coerced, err)
	}
	return coerced, nil
}
