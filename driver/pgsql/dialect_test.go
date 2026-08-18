package pgsql

import "testing"

func TestBind(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "parameters",
			query: `SELECT * FROM records WHERE tenant = ? AND id IN (?, ?)`,
			want:  `SELECT * FROM records WHERE tenant = $1 AND id IN ($2, $3)`,
		},
		{
			name:  "quoted markers",
			query: `SELECT '?' AS single, "?" AS double WHERE id = ?`,
			want:  `SELECT '?' AS single, "?" AS double WHERE id = $1`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := bind(test.query); got != test.want {
				t.Fatalf("bind(%q) = %q, want %q", test.query, got, test.want)
			}
		})
	}
}
