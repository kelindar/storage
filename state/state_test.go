package state

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func testMachine() Machine {
	return Machine{
		"create": "* -> requested",
		"update": "requested, rejected -> approved",
		"delete": "requested -> rejected",
		"run":    "rejected -> requested",
	}
}

func TestEdgeValue(t *testing.T) {
	tests := map[string]struct {
		edge      Edge
		wantSrc   []string
		wantState string
	}{
		"single source": {
			edge:      "requested -> rejected",
			wantSrc:   []string{"requested"},
			wantState: "rejected",
		},
		"multiple sources": {
			edge:      "requested, rejected -> approved",
			wantSrc:   []string{"requested", "rejected"},
			wantState: "approved",
		},
		"wildcard source": {
			edge:      "* -> requested",
			wantSrc:   []string{"*"},
			wantState: "requested",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			src, dst := tc.edge.Value()
			assert.Equal(t, tc.wantSrc, src)
			assert.Equal(t, tc.wantState, dst)
		})
	}
}

func TestEdgeValuePanics(t *testing.T) {
	tests := map[string]struct {
		edge Edge
	}{
		"empty":       {edge: ""},
		"missing dst": {edge: "requested ->"},
		"missing src": {edge: "-> approved"},
		"no arrow":    {edge: "requested approved"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, func() {
				tc.edge.Value()
			})
		})
	}
}

func TestMachineDefault(t *testing.T) {
	tests := map[string]struct {
		machine Machine
		want    string
	}{
		"wildcard create edge": {
			machine: Machine{
				"create": "* -> requested",
			},
			want: "requested",
		},
		"no wildcard edge": {
			machine: Machine{
				"update": "requested, rejected -> approved",
				"delete": "requested -> rejected",
				"run":    "rejected -> requested",
			},
			want: "",
		},
		"empty machine": {
			machine: Machine{},
			want:    "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.machine.Default())
		})
	}
}

func TestMachineTryAction(t *testing.T) {
	fsm := testMachine()

	tests := map[string]struct {
		action string
		from   string
		want   string
		ok     bool
	}{
		"update from rejected": {
			action: "update",
			from:   "rejected",
			want:   "approved",
			ok:     true,
		},
		"delete from requested": {
			action: "delete",
			from:   "requested",
			want:   "rejected",
			ok:     true,
		},
		"run from rejected": {
			action: "run",
			from:   "rejected",
			want:   "requested",
			ok:     true,
		},
		"create does not match wildcard source": {
			action: "create",
			from:   "rejected",
			want:   "rejected",
			ok:     false,
		},
		"unknown action": {
			action: "read",
			from:   "requested",
			want:   "requested",
			ok:     false,
		},
		"valid action invalid source": {
			action: "delete",
			from:   "approved",
			want:   "approved",
			ok:     false,
		},
		"case insensitive source": {
			action: "update",
			from:   "REQUESTED",
			want:   "approved",
			ok:     true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out, ok := fsm.TryAction(tc.action, tc.from)
			assert.Equal(t, tc.want, out)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestMachineCanTransition(t *testing.T) {
	fsm := testMachine()

	tests := map[string]struct {
		from   string
		to     string
		wantOK bool
	}{
		"requested to approved": {
			from:   "requested",
			to:     "approved",
			wantOK: true,
		},
		"requested to rejected": {
			from:   "requested",
			to:     "rejected",
			wantOK: true,
		},
		"same state": {
			from:   "approved",
			to:     "approved",
			wantOK: true,
		},
		"no transition": {
			from:   "approved",
			to:     "requested",
			wantOK: false,
		},
		"case insensitive source": {
			from:   "REJECTED",
			to:     "requested",
			wantOK: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.wantOK, fsm.CanTransition(tc.from, tc.to))
		})
	}
}

func TestMachineStates(t *testing.T) {
	tests := map[string]struct {
		machine Machine
		want    []string
	}{
		"workflow machine": {
			machine: testMachine(),
			want:    []string{"requested", "approved", "rejected"},
		},
		"single destination": {
			machine: Machine{
				"create": "* -> draft",
			},
			want: []string{"draft"},
		},
		"empty machine": {
			machine: Machine{},
			want:    []string{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := tc.machine.States()
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			assert.Equal(t, want, got)
		})
	}
}

func BenchmarkMachine(b *testing.B) {
	fsm := testMachine()
	edge := Edge("requested, rejected -> approved")

	b.Run("EdgeValue", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			edge.Value()
		}
	})

	b.Run("TryAction", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			fsm.TryAction("update", "rejected")
		}
	})

	b.Run("CanTransition", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			fsm.CanTransition("requested", "approved")
		}
	})

	b.Run("Default", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			fsm.Default()
		}
	})

	b.Run("States", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = fsm.States()
		}
	})
}
