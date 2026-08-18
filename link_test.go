package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLink(t *testing.T) {
	source := URN{Tenant: "acme", Namespace: "system", Kind: "agent", ID: "aaaaaaaaaaaaaaaaaaaa"}
	target := URN{Tenant: "acme", Namespace: "system", Kind: "tool", ID: "bbbbbbbbbbbbbbbbbbbb"}

	t.Run("own", func(t *testing.T) {
		assert.Equal(t, Link{
			Source: source,
			Target: target,
			Path:   "resources.0",
			Kind:   LinkOwnership,
		}, Own(source, target, "resources.0"))
	})

	t.Run("use", func(t *testing.T) {
		assert.Equal(t, Link{
			Source: source,
			Target: target,
			Path:   "agent",
			Kind:   LinkDependency,
		}, Use(source, target, "agent"))
	})
}
