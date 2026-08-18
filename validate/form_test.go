package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type formItem struct {
	ID    string `json:"id" form:"ro"`
	Value string `json:"value" form:"rw"`
}

type formRecord struct {
	Name      string              `json:"name" form:"rw"`
	Type      string              `json:"type" form:"create"`
	Generated string              `json:"generated" form:"ro"`
	Items     []formItem          `json:"items" form:"rw"`
	Labels    map[string]formItem `json:"labels" form:"rw"`
}

func TestCreate(t *testing.T) {
	assert.NoError(t, Create(&formRecord{Name: "sample", Type: "report"}))

	err := Create(&formRecord{
		Generated: "server value",
		Items:     []formItem{{ID: "generated item"}},
	})
	errs := requireFormErrors(t, err, 2)
	assert.Equal(t, []string{"generated"}, errs[0].Path)
	assert.Equal(t, "generated is read-only", errs[0].Message())
	assert.Equal(t, "readonly", errs[0].Validator)
	assert.Equal(t, []string{"items", "0", "id"}, errs[1].Path)
}

func TestUpdate(t *testing.T) {
	current := &formRecord{
		Type:      "report",
		Generated: "server value",
		Items:     []formItem{{ID: "item-1"}},
		Labels:    map[string]formItem{"env": {ID: "label-1"}},
	}

	t.Run("allows unchanged and empty fields", func(t *testing.T) {
		assert.NoError(t, Update(current, &formRecord{
			Type:      current.Type,
			Generated: current.Generated,
			Items:     []formItem{{ID: "item-1"}},
			Labels:    map[string]formItem{"env": {ID: "label-1"}},
		}))
		assert.NoError(t, Update(current, &formRecord{}))
	})

	t.Run("rejects read-only changes", func(t *testing.T) {
		err := Update(current, &formRecord{
			Type:      "dashboard",
			Generated: "changed",
			Labels:    map[string]formItem{"env": {ID: "changed"}},
		})
		errs := requireFormErrors(t, err, 3)
		assert.Equal(t, []string{"type"}, errs[0].Path)
		assert.Equal(t, []string{"generated"}, errs[1].Path)
		assert.Equal(t, []string{"labels", "env", "id"}, errs[2].Path)
	})

	t.Run("clears unstored projections", func(t *testing.T) {
		incoming := &formRecord{
			Generated: "projection",
			Items:     []formItem{{ID: "projection"}},
		}
		require.NoError(t, Update(&formRecord{}, incoming))
		assert.Empty(t, incoming.Generated)
		assert.Empty(t, incoming.Items[0].ID)
	})

	t.Run("does not clear projections when validation fails", func(t *testing.T) {
		incoming := &formRecord{Type: "dashboard", Generated: "projection"}
		require.Error(t, Update(current, incoming))
		assert.Equal(t, "projection", incoming.Generated)
	})
}

func TestFormGuards(t *testing.T) {
	assert.Error(t, Create(nil))
	assert.Error(t, Create(formRecord{}))
	assert.Error(t, Create(&[]formRecord{}))
	assert.Error(t, Update(&formRecord{}, nil))
	assert.Error(t, Update(&formRecord{}, &struct{}{}))
}

func requireFormErrors(t *testing.T, err error, count int) []Error {
	t.Helper()
	var failures Errors
	require.ErrorAs(t, err, &failures)
	errs := failures.Errors()
	require.Len(t, errs, count)
	return errs
}
