package convert

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTitleCase(t *testing.T) {
	tests := map[string]string{
		"":                 "",
		"lowercase":        "Lowercase",
		"Class":            "Class",
		"MyClass":          "My Class",
		"MyC":              "My C",
		"HTML":             "HTML",
		"PDFLoader":        "PDF Loader",
		"AString":          "A String",
		"SimpleXMLParser":  "Simple XML Parser",
		"vimRPCPlugin":     "Vim RPC Plugin",
		"GL11Version":      "GL 11 Version",
		"99Bottles":        "99 Bottles",
		"May5":             "May 5",
		"BFG9000":          "BFG 9000",
		"BöseÜberraschung": "Böse Überraschung",
		"Job title":        "Job Title",
		"jobTitle":         "Job Title",
		"job_title":        "Job Title",
		"job-title":        "Job Title",
		"_leading":         "Leading",
		"Two  Spaces":      "Two Spaces",
	}

	for input, expected := range tests {
		assert.Equal(t, expected, TitleCase(input), "input: %s", input)
	}
}

func TestSlugLabel(t *testing.T) {
	tests := map[string]string{
		"github":     "Github",
		"git_hub":    "Git Hub",
		"git-hub":    "Git Hub",
		"slack":      "Slack",
		"":           "",
		"  github  ": "Github",
	}
	for input, want := range tests {
		assert.Equal(t, want, SlugLabel(input), input)
	}
}

func TestParse(t *testing.T) {
	assert.Equal(t, 1, Int("1", 0))
	assert.Equal(t, 0, Int("a", 0))
	assert.Equal(t, 1.0, Float("1", 0))
	assert.Equal(t, 0.0, Float("a", 0))
}

func TestStrings(t *testing.T) {
	assert.Nil(t, Strings(nil))
	assert.Empty(t, Strings([]string{}))
	assert.Equal(t, []string{"alpha", "beta"}, Strings([]string{" beta ", "alpha", "alpha", ""}))
}

func TestLabel(t *testing.T) {
	assert.Equal(t, "My Class", Label("pkg.MyClass"))
	assert.Equal(t, "Lowercase", Label("lowercase"))
}

func TestNumeric(t *testing.T) {
	t.Run("int64", func(t *testing.T) {
		tests := []struct {
			name  string
			value any
			want  int64
			err   bool
		}{
			{"float64", float64(1.5), 1, false},
			{"float32", float32(2.5), 2, false},
			{"int", 3, 3, false},
			{"int64", int64(4), 4, false},
			{"json", json.Number("5"), 5, false},
			{"string", "42", 42, false},
			{"nil", nil, 0, false},
			{"bad json", json.Number("bad"), 0, true},
			{"bad string", "bad", 0, true},
			{"unsupported", struct{}{}, 0, true},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := Int64(tc.value)
				if tc.err {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			})
		}
	})

	t.Run("uint64", func(t *testing.T) {
		tests := []struct {
			name  string
			value any
			want  uint64
			err   bool
		}{
			{"float64", float64(1.5), 1, false},
			{"float32", float32(2.5), 2, false},
			{"int", 3, 3, false},
			{"int64", int64(4), 4, false},
			{"uint64", uint64(5), 5, false},
			{"json", json.Number("6"), 6, false},
			{"string", "7", 7, false},
			{"nil", nil, 0, false},
			{"bad json", json.Number("bad"), 0, true},
			{"bad string", "bad", 0, true},
			{"unsupported", struct{}{}, 0, true},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := Uint64(tc.value)
				if tc.err {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			})
		}
	})

	t.Run("float64", func(t *testing.T) {
		tests := []struct {
			name  string
			value any
			want  float64
			err   bool
		}{
			{"float64", 1.5, 1.5, false},
			{"float32", float32(2.5), 2.5, false},
			{"int", 3, 3, false},
			{"int64", int64(4), 4, false},
			{"json", json.Number("5.5"), 5.5, false},
			{"string", "3.14", 3.14, false},
			{"nil", nil, 0, false},
			{"bad json", json.Number("bad"), 0, true},
			{"bad string", "bad", 0, true},
			{"unsupported", struct{}{}, 0, true},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, err := Float64(tc.value)
				if tc.err {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			})
		}
	})
}

func TestScheduleLabel(t *testing.T) {
	tests := []struct {
		name string
		cron string
		tz   string
		want string
	}{
		{name: "every minute", cron: "* * * * *", tz: "UTC", want: "every minute UTC"},
		{name: "every five minutes", cron: "*/5 * * * *", want: "every 5 minutes"},
		{name: "hourly", cron: "30 * * * *", want: "hourly at :30"},
		{name: "daily", cron: "0 8 * * *", tz: "America/New_York", want: "daily 08:00 America/New_York"},
		{name: "weekly", cron: "0 9 * * 1", want: "monday 09:00"},
		{name: "sunday zero", cron: "0 9 * * 0", want: "sunday 09:00"},
		{name: "sunday seven", cron: "0 9 * * 7", want: "sunday 09:00"},
		{name: "weekly fallback", cron: "0 9 * * 8", want: "weekly 09:00"},
		{name: "monthly", cron: "0 0 15 * *", want: "monthly day 15 00:00"},
		{name: "month field", cron: "0 0 1 6 *", want: "0 0 1 6 *"},
		{name: "invalid field count", cron: "* * * *", tz: "UTC", want: "* * * * UTC"},
		{name: "empty", cron: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ScheduleLabel(tt.cron, tt.tz))
		})
	}
}

func TestTitleHelpers(t *testing.T) {
	assert.Equal(t, "", titleWord(""))
	invalid := string([]byte{'i', 'n', 'v', 0xff})
	assert.Equal(t, []string{invalid}, splitCase(invalid))
}

func TestBuiltinID(t *testing.T) {
	first := BuiltinID("acme", "cortex")
	second := BuiltinID("acme", "cortex")
	other := BuiltinID("beta", "cortex")

	assert.Len(t, first, 20)
	assert.Equal(t, first, second)
	assert.NotEqual(t, first, other)
	assert.Regexp(t, `^[0-9a-v]{20}$`, first)
}
