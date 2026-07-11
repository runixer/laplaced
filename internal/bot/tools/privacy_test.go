package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestPerformPrivacyMode(t *testing.T) {
	userID := storage.ScopeID("123")

	tests := []struct {
		name       string
		action     string
		setEnabled *bool // expected SetPrivacyMode arg; nil = no call
		wantErr    bool
		wantSubstr string
	}{
		{"enable", "enable", testutil.Ptr(true), false, "ENABLED"},
		{"disable", "disable", testutil.Ptr(false), false, "DISABLED"},
		{"invalid action", "pause", nil, true, ""},
		{"missing action", "", nil, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := new(testutil.MockStorage)
			if tt.setEnabled != nil {
				mockStore.On("SetPrivacyMode", userID, *tt.setEnabled).Return(nil).Once()
			}
			e := &ToolExecutor{userRepo: mockStore, logger: testutil.TestLogger()}

			args := map[string]interface{}{}
			if tt.action != "" {
				args["action"] = tt.action
			}
			out, err := e.performPrivacyMode(userID, args)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Contains(t, out, tt.wantSubstr)
			}
			mockStore.AssertExpectations(t)
		})
	}
}

func TestPerformPrivacyMode_NotConfigured(t *testing.T) {
	e := &ToolExecutor{logger: testutil.TestLogger()}
	_, err := e.performPrivacyMode("123", map[string]interface{}{"action": "enable"})
	assert.Error(t, err)
}
