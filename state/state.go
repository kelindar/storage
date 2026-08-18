package state

import (
	"fmt"
	"regexp"
	"strings"
)

// Shared resource lifecycle states. Domain-specific state machines add only
// states that do not describe a general resource lifecycle.
const (
	Creating = "creating"
	Active   = "active"
	Inactive = "inactive"
	Deleting = "deleting"
	Failed   = "failed"
)

var edgeExpr = regexp.MustCompile(`^([a-zA-Z_\*, ]+)(\s->\s)([a-zA-Z_]+)$`)

// Edge represents a state transition edge. The transition must be a string with
// an arrow, for example "requested -> approved".
type Edge string

// Value parses the edge and returns a source and destination pair.
func (e Edge) Value() ([]string, string) {
	groups := edgeExpr.FindStringSubmatch(string(e))
	if len(groups) != 4 {
		panic(fmt.Errorf("edge '%v' is not in 'source -> destination' format", e))
	}

	sources := strings.Split(groups[1], ",")
	for i, src := range sources {
		sources[i] = strings.TrimSpace(src)
	}
	return sources, groups[3]
}

// Machine represents a finite state machine keyed by action name.
type Machine map[string]Edge

// Default returns the default state. If no default state was configured it
// returns an empty string.
func (m Machine) Default() string {
	for _, rule := range m {
		if src, dst := rule.Value(); len(src) == 1 && src[0] == "*" {
			return dst
		}
	}
	return ""
}

// TryAction attempts to transition from a current state.
func (m Machine) TryAction(action, current string) (string, bool) {
	edge, ok := m[action]
	if !ok {
		return current, false
	}

	src, dst := edge.Value()
	for _, s := range src {
		if strings.EqualFold(s, current) {
			return dst, true
		}
	}
	return current, false
}

// CanTransition reports whether a transition from one state to another is allowed.
func (m Machine) CanTransition(from, to string) bool {
	if from == to {
		return true
	}

	for _, rule := range m {
		src, dst := rule.Value()
		if dst != to {
			continue
		}
		for _, s := range src {
			if strings.EqualFold(s, from) {
				return true
			}
		}
	}
	return false
}

// States returns a list of all possible states in the machine.
func (m Machine) States() []string {
	seen := make(map[string]struct{}, len(m))
	for _, rule := range m {
		_, dst := rule.Value()
		seen[dst] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}
