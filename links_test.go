package storage

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type linkObject struct {
	Meta   `json:",inline"`
	target URN
}

type dynamicObject struct {
	Meta  `json:",inline"`
	Value any `json:"value"`
}

type dynamicTarget struct {
	Target URN `json:"target" link:"artifact"`
}

type stringObject struct {
	Meta   `json:",inline"`
	Target string `json:"target" link:"artifact"`
}

type inlineObject struct {
	Meta `json:",inline"`
	Spec inlineSpec `json:"spec"`
}

type inlineSpec struct {
	Config inlineConfig `json:",inline"`
}

type inlineConfig struct {
	Target URN `json:"target" link:"artifact"`
}

type conversation struct {
	Meta     `kind:"conversation" json:",inline"`
	Messages []conversationMessage `json:"messages"`
}

type conversationMessage struct {
	Attachments []URN `json:"attachments" link:"blob"`
}

type mapObject struct {
	Meta  `json:",inline"`
	Value map[string]dynamicTarget `json:"value"`
}

type invalidObject struct {
	Meta   `json:",inline"`
	Target string `json:"target" link:"artifact"`
}

type errorLinker struct {
	Meta `json:",inline"`
}

func (*errorLinker) Links() ([]Link, error) {
	return nil, errors.New("linker failed")
}

func (o *linkObject) Links() ([]Link, error) {
	return []Link{Use(o.URN(), o.target, "target")}, nil
}

func TestLinks(t *testing.T) {
	source := testURN(t, "conversation", "00000000000000000000")
	first := testURN(t, KindBlob, "00000000000000000001")
	second := testURN(t, KindBlob, "00000000000000000002")
	conversation := &conversation{
		Meta: Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
		Messages: []conversationMessage{
			{Attachments: []URN{first, second}},
		},
	}

	links, err := Links(conversation)
	require.NoError(t, err)
	assert.Equal(t, []Link{
		Use(source, first, "messages.0.attachments.0"),
		Use(source, second, "messages.0.attachments.1"),
	}, links)
}

func TestLinksUsesLinker(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")
	obj := &linkObject{Meta: Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID}, target: target}

	links, err := Links(obj)
	require.NoError(t, err)
	assert.Equal(t, []Link{Use(source, target, "target")}, links)
}

func TestLinksUsesString(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")
	obj := &stringObject{
		Meta:   Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
		Target: target.String(),
	}

	links, err := Links(obj)
	require.NoError(t, err)
	assert.Equal(t, []Link{Use(source, target, "target")}, links)
}

func TestLinksErrors(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")

	_, err := Links(&invalidObject{
		Meta:   Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
		Target: "not-a-urn",
	})
	assert.ErrorContains(t, err, "invalid link")

	_, err = Links(&errorLinker{Meta: Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID}})
	assert.ErrorContains(t, err, "linker failed")
}

func TestLinkInfo(t *testing.T) {
	first := linkInfo(reflect.TypeOf(dynamicTarget{}))
	second := linkInfo(reflect.TypeOf(dynamicTarget{}))

	assert.Same(t, first, second)
	require.Len(t, first.fields, 1)
	assert.Equal(t, Kind("artifact"), first.fields[0].kind)
}

func TestLinksWalk(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")

	tests := map[string]Object{
		"dynamic": &dynamicObject{
			Meta:  Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
			Value: dynamicTarget{Target: target},
		},
		"inline": &inlineObject{
			Meta: Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
			Spec: inlineSpec{Config: inlineConfig{Target: target}},
		},
		"map": &mapObject{
			Meta:  Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
			Value: map[string]dynamicTarget{"entry": {Target: target}},
		},
	}
	want := map[string][]Link{
		"dynamic": {Use(source, target, "value.target")},
		"inline":  {Use(source, target, "spec.target")},
		"map":     {Use(source, target, "value.entry.target")},
	}

	for name, obj := range tests {
		t.Run(name, func(t *testing.T) {
			links, err := Links(obj)
			require.NoError(t, err)
			assert.Equal(t, want[name], links)
		})
	}
}

func TestLinkHelpers(t *testing.T) {
	t.Run("options", func(t *testing.T) {
		assert.True(t, hasOption("inline,required", "inline"))
		assert.True(t, hasOption("required,inline", "inline"))
		assert.False(t, hasOption("required", "inline"))
	})

	t.Run("map keys", func(t *testing.T) {
		tests := []struct {
			value any
			want  string
		}{
			{"name", "name"},
			{true, "true"},
			{int8(-2), "-2"},
			{uint16(3), "3"},
			{float32(1.5), "1.5"},
			{struct{ Name string }{"x"}, "{x}"},
		}
		for _, tc := range tests {
			key := reflect.ValueOf(tc.value)
			assert.Equal(t, tc.want, string(appendMapKey(nil, key)))
			assert.Equal(t, tc.want, mapKeyString(key))
		}
	})

	t.Run("comparisons", func(t *testing.T) {
		a := URN{Tenant: "a", Namespace: "a", Kind: "a", ID: "a"}
		b := URN{Tenant: "b", Namespace: "a", Kind: "a", ID: "a"}
		assert.Equal(t, -1, compareURN(a, b))
		assert.Equal(t, 1, compareURN(b, a))
		assert.Equal(t, -1, compareURN(a, URN{Tenant: "a", Namespace: "b"}))
		assert.Equal(t, -1, compareURN(a, URN{Tenant: "a", Namespace: "a", Kind: "b"}))
		assert.Equal(t, -1, compareURN(a, URN{Tenant: "a", Namespace: "a", Kind: "a", ID: "b"}))
		assert.Equal(t, 0, compareURN(a, a))
		assert.Equal(t, -1, compareLink(Link{Path: "a", Target: b}, Link{Path: "b", Target: a}))
		assert.Equal(t, 1, compareLink(Link{Path: "b", Target: a}, Link{Path: "a", Target: b}))
		assert.Equal(t, -1, compareLink(Link{Path: "a", Target: a}, Link{Path: "a", Target: b}))
		assert.Equal(t, -1, compareString("a", "b"))
		assert.Equal(t, 1, compareString("b", "a"))
		assert.Equal(t, 0, compareString("a", "a"))
	})

	t.Run("presence", func(t *testing.T) {
		assert.True(t, containsLinks(reflect.ValueOf(dynamicTarget{})))
		assert.True(t, containsLinks(reflect.ValueOf(map[string]dynamicTarget{"x": {}})))
		assert.False(t, containsLinks(reflect.Value{}))
		assert.False(t, containsLinks(reflect.ValueOf((*dynamicTarget)(nil))))
		assert.False(t, containsLinks(reflect.ValueOf([]dynamicTarget{})))
		assert.False(t, containsLinks(reflect.ValueOf(struct{}{})))

		assert.True(t, emptyLink(reflect.ValueOf(URN{})))
		assert.False(t, emptyLink(reflect.ValueOf(testURN(t, "artifact", "00000000000000000001"))))
		assert.True(t, emptyLink(reflect.ValueOf("")))
		assert.False(t, emptyLink(reflect.ValueOf("value")))
		assert.True(t, emptyLink(reflect.ValueOf([]string{})))
		assert.False(t, emptyLink(reflect.ValueOf([1]string{"value"})))
		assert.False(t, emptyLink(reflect.ValueOf(1)))
		assert.True(t, hasLink(reflect.TypeOf(dynamicTarget{})))
		assert.False(t, hasLink(reflect.TypeOf(struct{}{})))
		assert.Nil(t, linkInfo(nil))
	})

	t.Run("scan types", func(t *testing.T) {
		type recursive struct {
			Child  *recursive
			Target URN `link:"artifact"`
		}
		assert.Equal(t, 1, scanLinks(reflect.TypeOf(recursive{}), nil))
		assert.Equal(t, 1, scanLinks(reflect.TypeOf([]dynamicTarget{}), nil))
		assert.Equal(t, 1, scanLinks(reflect.TypeOf(map[string]dynamicTarget{}), nil))
		assert.Equal(t, 1, scanLinks(reflect.TypeOf((*interface{})(nil)).Elem(), nil))
		assert.Zero(t, scanLinks(reflect.TypeOf(1), nil))

		type fields struct {
			Target  URN    `link:"artifact"`
			Ignored string `json:"-" link:"artifact"`
			hidden  string
			Plain   int
		}
		assert.Len(t, scanFields(reflect.TypeOf(fields{})), 1)
	})

	t.Run("walk values", func(t *testing.T) {
		source := testURN(t, "app", "00000000000000000000")
		var links []Link
		assert.NoError(t, walkLinks(reflect.ValueOf(1), nil, source, &links))
		assert.NoError(t, walkLinks(reflect.ValueOf((*dynamicTarget)(nil)), nil, source, &links))
		assert.NoError(t, walkLinks(reflect.Value{}, nil, source, &links))
		assert.NoError(t, extractValue(reflect.ValueOf([]string{}), []byte("target"), source, "artifact", &links))
		assert.ErrorContains(t, extractValue(reflect.ValueOf([]string{"bad"}), []byte("target"), source, "artifact", &links), "invalid link")
		assert.ErrorContains(t, extractValue(reflect.ValueOf(map[string]string{"x": "bad"}), []byte("target"), source, "artifact", &links), "invalid link")
	})
}

func BenchmarkLinks(b *testing.B) {
	source := testURN(b, "conversation", "00000000000000000000")
	first := testURN(b, KindBlob, "00000000000000000001")
	second := testURN(b, KindBlob, "00000000000000000002")
	conversation := &conversation{
		Meta: Meta{Tenant: source.Tenant, Namespace: source.Namespace, Kind: source.Kind, ID: source.ID},
		Messages: []conversationMessage{
			{Attachments: []URN{first, second}},
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

var benchLinks []Link

func TestExtractTagged(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")
	wrongKind := testURN(t, "deployment", "00000000000000000002")

	tests := map[string]struct {
		value   any
		want    []Link
		wantErr string
	}{
		"extracts urn": {
			value: target,
			want:  []Link{Use(source, target, "target")},
		},
		"extracts string": {
			value: target.String(),
			want:  []Link{Use(source, target, "target")},
		},
		"extracts pointer and interface": {
			value: any(&target),
			want:  []Link{Use(source, target, "target")},
		},
		"extracts slices and sorted maps": {
			value: map[string]any{
				"z": target,
				"a": []string{target.String()},
			},
			want: []Link{
				Use(source, target, "target.a.0"),
				Use(source, target, "target.z"),
			},
		},
		"skips nil and zero values": {
			value: []any{(*URN)(nil), URN{}, ""},
		},
		"rejects invalid string": {value: "not-a-urn", wantErr: "invalid link at target"},
		"rejects wrong type":     {value: 1, wantErr: "must be a URN or string"},
		"rejects wrong kind":     {value: wrongKind, wantErr: "invalid link at target"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var got []Link
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

func testURN(t testing.TB, kind Kind, id string) URN {
	t.Helper()
	urn, err := MakeURN("acme", "system", kind, id)
	require.NoError(t, err)
	return urn
}
