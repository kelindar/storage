package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestActor(t *testing.T) {
	tests := map[string]struct {
		ctx  context.Context
		want string
	}{
		"nil context": {
			want: UnknownActor,
		},
		"empty context": {
			ctx:  context.Background(),
			want: UnknownActor,
		},
		"empty actor defaults to unknown": {
			ctx:  WithActor(context.Background(), ""),
			want: UnknownActor,
		},
		"carries actor": {
			ctx:  WithActor(context.Background(), "alice"),
			want: "alice",
		},
		"system actor": {
			ctx:  WithActor(context.Background(), SystemActor),
			want: SystemActor,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, Actor(tt.ctx))
		})
	}
}
