package sqlite

import (
	"testing"

	"github.com/kelindar/storage"
	"github.com/stretchr/testify/require"
)

func TestValidateLinks(t *testing.T) {
	source := testURN(t, "app", "00000000000000000000")
	target := testURN(t, "artifact", "00000000000000000001")
	blob := testURN(t, "blob", "00000000000000000002")
	bundle := testURN(t, "bundle", "00000000000000000003")
	valid := storage.Use(source, target, "target")

	tests := map[string]struct {
		source  storage.URN
		links   []storage.Link
		wantErr string
	}{
		"accepts valid link":         {links: []storage.Link{valid}},
		"rejects mismatched source":  {links: []storage.Link{storage.Use(target, target, "target")}, wantErr: "source mismatch"},
		"rejects invalid target":     {links: []storage.Link{storage.Use(source, storage.URN{}, "target")}, wantErr: "target is invalid"},
		"requires path":              {links: []storage.Link{storage.Use(source, target, "")}, wantErr: "path is required"},
		"rejects invalid kind":       {links: []storage.Link{{Source: source, Target: target, Path: "target"}}, wantErr: "kind is invalid"},
		"rejects duplicate":          {links: []storage.Link{valid, valid}, wantErr: "duplicate link"},
		"rejects cross tenant":       {links: []storage.Link{storage.Use(source, targetURN(t, "other", "artifact", "00000000000000000002"), "target")}, wantErr: "crosses tenants"},
		"allows Bundle ownership":    {source: bundle, links: []storage.Link{storage.Own(bundle, target, "resources.0")}},
		"allows Blob ownership":      {links: []storage.Link{storage.Own(source, blob, "blob")}},
		"rejects non-Blob ownership": {links: []storage.Link{storage.Own(source, target, "target")}, wantErr: "only Bundles may own non-Blob resources"},
		"rejects Blob ownership":     {source: blob, links: []storage.Link{storage.Own(blob, target, "target")}, wantErr: "blob cannot own resources"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if tc.source == (storage.URN{}) {
				tc.source = source
			}
			err := validateLinks(tc.source, tc.links)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
