package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProviderRoutingConfig_ToRouting(t *testing.T) {
	falseVal := false

	tests := []struct {
		name  string
		cfg   ProviderRoutingConfig
		isNil bool
	}{
		{
			name:  "empty config returns nil",
			cfg:   ProviderRoutingConfig{},
			isNil: true,
		},
		{
			name:  "order only produces non-nil routing",
			cfg:   ProviderRoutingConfig{Order: []string{"Google"}},
			isNil: false,
		},
		{
			name:  "allow_fallbacks alone produces non-nil routing",
			cfg:   ProviderRoutingConfig{AllowFallbacks: &falseVal},
			isNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.ToRouting()
			if tt.isNil {
				assert.Nil(t, got)
				return
			}
			assert.NotNil(t, got)
			assert.Equal(t, tt.cfg.Order, got.Order)
			assert.Equal(t, tt.cfg.AllowFallbacks, got.AllowFallbacks)
		})
	}
}
