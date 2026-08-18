package walk

import (
	"reflect"
	"testing"

	"github.com/kelindar/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type linkObject struct {
	storage.Meta `json:",inline"`
	target       storage.URN
}

type dynamicObject struct {
	storage.Meta `json:",inline"`
	Value        any `json:"value"`
}

type dynamicTarget struct {
	Target storage.URN `json:"target" link:"artifact"`
}

type stringObject struct {
	storage.Meta `json:",inline"`
	Target       string `json:"target" link:"artifact"`
}

type inlineObject struct {
	storage.Meta `json:",inline"`
	Spec         inlineSpec `json:"spec"`
}

type inlineSpec struct {
	Config inlineConfig `json:",inline"`
}

type inlineConfig struct {
	Target storage.URN `json:"target" link:"artifact"`
}

type conversation struct {
	storage.Meta `kind:"conversation" json:",inline"`
	Messages     []conversationMessage `json:"messages"`
}

type conversationMessage struct {
	Attachments []storage.URN `json:"attachments" link:"blob"`
}

func (o *linkObject) Links() ([]storage.Link, error) {
	return []storage.Link{storage.Use(o.URN(), o.target, "target")}, nil
}

func TestLinks(t *testing.T) {
	source := testURN(t, "conversation", "00000000000000000000")
	first := testURN(t, storage.KindBlob, "00000000000000000001")
	second := testURN(t, storage.KindBlob, "00000000000000000002")
	conversation := &conversation{
		Meta: storage.Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
		Messages: []conversationMessage{
			{Attachments: []storage.URN{first, second}},
		},
	}

	links, err := Links(conversation)
	require.NoError(t, err)
	assert.Equal(t, []storage.Link{
		storage.Use(source, first, "messages.0.attachments.0"),
		storage.Use(source, second, "messages.0.attachments.1"),
	}, links)
}

func TestLinksUsesLinker(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")
	obj := &linkObject{Meta: storage.Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID}, target: target}

	links, err := Links(obj)
	require.NoError(t, err)
	assert.Equal(t, []storage.Link{storage.Use(source, target, "target")}, links)
}

func TestLinksUsesString(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")
	obj := &stringObject{
		Meta:   storage.Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
		Target: target.String(),
	}

	links, err := Links(obj)
	require.NoError(t, err)
	assert.Equal(t, []storage.Link{storage.Use(source, target, "target")}, links)
}

func TestLinkInfo(t *testing.T) {
	first := linkInfo(reflect.TypeOf(dynamicTarget{}))
	second := linkInfo(reflect.TypeOf(dynamicTarget{}))

	assert.Same(t, first, second)
	require.Len(t, first.fields, 1)
	assert.Equal(t, storage.Kind("artifact"), first.fields[0].kind)
}

func TestLinksWalk(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")

	tests := map[string]storage.Object{
		"dynamic": &dynamicObject{
			Meta:  storage.Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
			Value: dynamicTarget{Target: target},
		},
		"inline": &inlineObject{
			Meta: storage.Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
			Spec: inlineSpec{Config: inlineConfig{Target: target}},
		},
	}
	want := map[string][]storage.Link{
		"dynamic": {storage.Use(source, target, "value.target")},
		"inline":  {storage.Use(source, target, "spec.target")},
	}

	for name, obj := range tests {
		t.Run(name, func(t *testing.T) {
			links, err := Links(obj)
			require.NoError(t, err)
			assert.Equal(t, want[name], links)
		})
	}
}

func BenchmarkLinks(b *testing.B) {
	source := testURN(b, "conversation", "00000000000000000000")
	first := testURN(b, storage.KindBlob, "00000000000000000001")
	second := testURN(b, storage.KindBlob, "00000000000000000002")
	conversation := &conversation{
		Meta: storage.Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
		Messages: []conversationMessage{
			{Attachments: []storage.URN{first, second}},
		},
	}
	b.ReportAllocs()
	for range b.N {
		var err error
		benchLinks, err = Links(conversation)
		if err != nil {
			b.Fatal(err)
		}
	}
}

var benchLinks []storage.Link

func TestExtractTagged(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")
	wrongKind := testURN(t, "deployment", "00000000000000000002")

	tests := map[string]struct {
		value   any
		want    []storage.Link
		wantErr string
	}{
		"extracts urn": {
			value: target,
			want:  []storage.Link{storage.Use(source, target, "target")},
		},
		"extracts string": {
			value: target.String(),
			want:  []storage.Link{storage.Use(source, target, "target")},
		},
		"extracts pointer and interface": {
			value: any(&target),
			want:  []storage.Link{storage.Use(source, target, "target")},
		},
		"extracts slices and sorted maps": {
			value: map[string]any{
				"z": target,
				"a": []string{target.String()},
			},
			want: []storage.Link{
				storage.Use(source, target, "target.a.0"),
				storage.Use(source, target, "target.z"),
			},
		},
		"skips nil and zero values": {
			value: []any{(*storage.URN)(nil), storage.URN{}, ""},
		},
		"rejects invalid string": {value: "not-a-urn", wantErr: "invalid link at target"},
		"rejects wrong type":     {value: 1, wantErr: "must be a URN or string"},
		"rejects wrong kind":     {value: wrongKind, wantErr: "invalid link at target"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got []storage.Link
			err := extractTagged(reflect.ValueOf(tc.value), []string{"target"}, source, "artifact", &got)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func testURN(t testing.TB, kind storage.Kind, id string) storage.URN {
	t.Helper()
	urn, err := storage.MakeURN("acme", "system", kind, id)
	require.NoError(t, err)
	return urn
}
