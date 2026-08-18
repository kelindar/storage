package storage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/rs/xid"
)

var (
	regexName = regexp.MustCompile(`^([a-z][a-z0-9\_]{1,19})$`) // 2-20 characters, starting with a letter
	regexID   = regexp.MustCompile(`^([0-9a-v]{20})$`)
)

// URN represents a uniform resource name for accessing resources.
// Format: urn:tenant:namespace:kind:id
type URN struct {
	Tenant    string `json:"-" uri:"tenant" binding:"required"`    // Tenant slug (e.g. "acme")
	Namespace string `json:"-" uri:"namespace" binding:"required"` // Namespace name (e.g. "default")
	Kind      Kind   `json:"-" uri:"kind" binding:"required"`      // Object kind (e.g. "namespace")
	ID        string `json:"-" uri:"id"`                           // Globally unique identifier
}

// NewURN creates a new URN with a generated id.
func NewURN(tenant, namespace string, kind Kind) (URN, error) {
	return makeURN(tenant, namespace, kind, "*")
}

// MakeURN assembles a URN from its parts.
func MakeURN(tenant, namespace string, kind Kind, id string) (URN, error) {
	return makeURN(tenant, namespace, kind, id)
}

func makeURN(tenant, namespace string, kind Kind, id string) (URN, error) {
	tenant = strings.ToLower(tenant)
	namespace = strings.ToLower(namespace)
	kind = Kind(strings.ToLower(string(kind)))

	switch {
	case !regexName.MatchString(tenant):
		return URN{}, fmt.Errorf("urn: invalid tenant name: %s", tenant)
	case !regexName.MatchString(namespace):
		return URN{}, fmt.Errorf("urn: invalid namespace name: %s", namespace)
	case !regexName.MatchString(string(kind)):
		return URN{}, fmt.Errorf("urn: invalid kind name: %s", kind)
	case id == "*":
		id = xid.New().String()
	case !regexID.MatchString(id):
		return URN{}, fmt.Errorf("urn: invalid id: %s", id)
	}

	return URN{
		Tenant:    tenant,
		Namespace: namespace,
		Kind:      kind,
		ID:        id,
	}, nil
}

// ParseURN parses a string into a URN.
func ParseURN(s string) (URN, error) {
	switch {
	case !strings.HasPrefix(s, "urn:"):
		return URN{}, fmt.Errorf("urn: invalid scheme %s", s)
	case s == "urn::::":
		return URN{}, nil
	case len(s) < 14:
		return URN{}, fmt.Errorf("urn: invalid length %s", s)
	}

	var decoded URN
	offset := 4 // Skip the "urn:" prefix
	cursor := 0 // Part of the URN we are parsing
	for i := 4; i < len(s); i++ {
		if s[i] != ':' {
			continue
		}

		switch cursor {
		case 0:
			decoded.Tenant = s[offset:i]
		case 1:
			decoded.Namespace = s[offset:i]
		case 2:
			decoded.Kind = Kind(s[offset:i])
		}
		offset = i + 1
		cursor++
	}

	decoded.ID = s[offset:]
	switch {
	case cursor != 3:
		return URN{}, fmt.Errorf("urn: invalid format %s", s)
	case !regexName.MatchString(decoded.Tenant):
		return URN{}, fmt.Errorf("urn: invalid tenant: %s", decoded.Tenant)
	case !regexName.MatchString(decoded.Namespace):
		return URN{}, fmt.Errorf("urn: invalid namespace: %s", decoded.Namespace)
	case !regexName.MatchString(string(decoded.Kind)):
		return URN{}, fmt.Errorf("urn: invalid kind name: %s", decoded.Kind)
	case decoded.ID == "*":
		decoded.ID = xid.New().String()
		return decoded, nil
	case !regexID.MatchString(decoded.ID):
		return URN{}, fmt.Errorf("urn: invalid id: %s", decoded.ID)
	default:
		return decoded, nil
	}
}

// String returns the string representation of the URN.
func (u URN) String() string {
	if u.Tenant == "" || u.Namespace == "" || u.Kind == "" || u.ID == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("urn:")
	sb.WriteString(u.Tenant)
	sb.WriteString(":")
	sb.WriteString(u.Namespace)
	sb.WriteString(":")
	sb.WriteString(string(u.Kind))
	sb.WriteString(":")
	sb.WriteString(u.ID)
	return sb.String()
}

// IsValid returns true if the URN is valid.
func (u URN) IsValid() bool {
	return regexName.MatchString(u.Tenant) &&
		regexName.MatchString(u.Namespace) &&
		regexName.MatchString(string(u.Kind)) &&
		regexID.MatchString(u.ID)
}

// MarshalJSON marshals the URN to JSON.
func (u URN) MarshalJSON() ([]byte, error) {
	return json.Marshal(u.String())
}

// UnmarshalJSON unmarshals the JSON into a URN.
func (u *URN) UnmarshalJSON(b []byte) error {
	var encoded string
	if err := json.Unmarshal(b, &encoded); err != nil {
		return err
	}

	if encoded == "" {
		return nil
	}

	decoded, err := ParseURN(encoded)
	if err != nil {
		return err
	}

	*u = decoded
	return nil
}
