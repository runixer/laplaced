package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
)

func TestBasicAuthMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		authEnabled    bool
		authUsername   string
		authPassword   string
		requestPath    string
		requestUser    string
		requestPass    string
		expectedStatus int
	}{
		{
			name:           "Auth Disabled - UI Route",
			authEnabled:    false,
			requestPath:    "/ui/stats",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Auth Enabled - UI Route - No Credentials",
			authEnabled:    true,
			authUsername:   "admin",
			authPassword:   "secret",
			requestPath:    "/ui/stats",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Auth Enabled - UI Route - Wrong Credentials",
			authEnabled:    true,
			authUsername:   "admin",
			authPassword:   "secret",
			requestPath:    "/ui/stats",
			requestUser:    "admin",
			requestPass:    "wrong",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Auth Enabled - UI Route - Correct Credentials",
			authEnabled:    true,
			authUsername:   "admin",
			authPassword:   "secret",
			requestPath:    "/ui/stats",
			requestUser:    "admin",
			requestPass:    "secret",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "Auth Enabled - Non-UI Route",
			authEnabled:    true,
			authUsername:   "admin",
			authPassword:   "secret",
			requestPath:    "/healthz",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Server.Auth.Enabled = tt.authEnabled
			cfg.Server.Auth.Username = tt.authUsername
			cfg.Server.Auth.Password = tt.authPassword

			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			// We don't need real dependencies for middleware test
			server := &Server{
				cfg:    cfg,
				logger: logger,
			}

			// Create a dummy handler that returns 200 OK
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			middleware := server.basicAuthMiddleware(nextHandler)

			req := httptest.NewRequest("GET", tt.requestPath, nil)
			if tt.requestUser != "" || tt.requestPass != "" {
				req.SetBasicAuth(tt.requestUser, tt.requestPass)
			}

			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code)
		})
	}
}

func TestServer_Start_GeneratesPassword(t *testing.T) {
	// Arrange
	cfg := &config.Config{}
	cfg.Server.Auth.Enabled = true
	cfg.Server.Auth.Password = "" // Empty, should be generated
	cfg.Server.ListenPort = "0"   // Random port

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)

	// Setup mocks for logging middleware
	mockBot.On("API").Return(mockAPI)
	mockAPI.On("GetToken").Return("test-token")

	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	// Act
	// Run Start in a goroutine because it blocks
	errChan := make(chan error)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Give it a moment to initialize and generate password
	time.Sleep(100 * time.Millisecond)
	cancel() // Stop the server

	// Wait for Start to return
	err = <-errChan
	assert.NoError(t, err)

	// Assert
	assert.NotEmpty(t, cfg.Server.Auth.Password, "Password should be generated")
	assert.Len(t, cfg.Server.Auth.Password, 12, "Password should be 12 hex characters")
}
