package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTarget(t *testing.T) {
	urn := "urn:acme:system:dataset:d8u3a29hq4up7f3odd10"
	for _, tc := range []struct {
		raw     string
		want    string
		ref     string
		version int
		latest  bool
		draft   bool
	}{
		{raw: urn, want: urn},
		{raw: urn + "@v01", want: urn + "@v1", ref: "v1", version: 1},
		{raw: urn + "@latest", want: urn + "@latest", ref: "latest", latest: true},
		{raw: urn + "@draft", want: urn + "@draft", ref: "draft", draft: true},
	} {
		target, err := ParseTarget(tc.raw)
		require.NoError(t, err)
		assert.Equal(t, tc.want, target.String())
		assert.Equal(t, tc.ref, target.Ref())
		assert.Equal(t, tc.version, target.Version())
		assert.Equal(t, tc.latest, target.IsLatest())
		assert.Equal(t, tc.draft, target.IsDraft())
		assert.Equal(t, urn, target.URN().String())

		data, err := json.Marshal(target)
		require.NoError(t, err)
		var decoded Target
		require.NoError(t, json.Unmarshal(data, &decoded))
		assert.Equal(t, target, decoded)
	}

	for _, raw := range []string{"", urn + "@", urn + "@v0", urn + "@v1@v2", urn + "@bad", urn + "@bad ref"} {
		_, err := ParseTarget(raw)
		require.Error(t, err)
	}
	for _, target := range []Target{Target(urn + "@v-1"), Target(urn + "@v+1"), Target(urn + "@v01")} {
		assert.Zero(t, target.Version())
	}

	var target Target
	require.Error(t, json.Unmarshal([]byte(`"not-a-target"`), &target))
	require.Error(t, json.Unmarshal([]byte(`123`), &target))
	_, err := json.Marshal(Target("not-a-target"))
	require.Error(t, err)
}
