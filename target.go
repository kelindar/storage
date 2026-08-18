package storage

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Target identifies a resource and an optional reference.
type Target string

// ParseTarget parses a target in the form <URN> or <URN>@<ref>.
func ParseTarget(raw string) (Target, error) {
	base, ref, hasRef := strings.Cut(strings.TrimSpace(raw), "@")
	urn, err := ParseURN(base)
	if err != nil {
		return "", fmt.Errorf("target: %w", err)
	}
	switch {
	case !hasRef:
		return Target(urn.String()), nil
	case ref == "draft" || ref == "latest":
		return Target(urn.String() + "@" + ref), nil
	case len(ref) < 2 || ref[0] != 'v':
		return "", fmt.Errorf("target: invalid reference %q", ref)
	}
	for _, r := range ref[1:] {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("target: invalid reference %q", ref)
		}
	}
	version, err := strconv.Atoi(ref[1:])
	if err != nil || version < 1 {
		return "", fmt.Errorf("target: invalid reference %q", ref)
	}
	return Target(fmt.Sprintf("%s@v%d", urn, version)), nil
}

// String returns the target string.
func (t Target) String() string { return string(t) }

// URN returns the resource portion of the target.
func (t Target) URN() URN {
	raw, _, _ := strings.Cut(string(t), "@")
	urn, _ := ParseURN(raw)
	return urn
}

// Ref returns the optional reference.
func (t Target) Ref() string {
	_, ref, _ := strings.Cut(string(t), "@")
	return ref
}

// Version returns the exact positive version, or zero for an unversioned selector.
func (t Target) Version() int {
	ref := t.Ref()
	if len(ref) < 2 || ref[0] != 'v' {
		return 0
	}
	version, err := strconv.Atoi(ref[1:])
	if err != nil || version < 1 || ref != "v"+strconv.Itoa(version) {
		return 0
	}
	return version
}

// IsLatest reports whether the target selects the latest version.
func (t Target) IsLatest() bool { return t.Ref() == "latest" }

// IsDraft reports whether the target selects the draft version.
func (t Target) IsDraft() bool { return t.Ref() == "draft" }

// MarshalJSON marshals a valid target as a JSON string.
func (t Target) MarshalJSON() ([]byte, error) {
	if t == "" {
		return json.Marshal("")
	}
	parsed, err := ParseTarget(t.String())
	if err != nil {
		return nil, err
	}
	return json.Marshal(parsed.String())
}

// UnmarshalJSON validates and unmarshals a target from a JSON string.
func (t *Target) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*t = ""
		return nil
	}
	parsed, err := ParseTarget(raw)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}
