package memory

import (
	"io"
	"log/slog"
	"os"
	"testing"

	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/testutil"
)

// testServices holds all mocked services for testing.
type testServices struct {
	Service    *Service
	Store      *testutil.MockStorage
	ORClient   *testutil.MockOpenRouterClient
	Agent      *agenttesting.MockAgent
	Logger     *slog.Logger
	Cfg        *config.Config
	Translator *i18n.Translator
}

// setupTestServices creates a fully configured memory service with mocks.
func setupTestServices(tb testing.TB) *testServices {
	tb.Helper()

	store := new(testutil.MockStorage)
	orClient := new(testutil.MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := testutil.TestConfig()
	translator, err := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	if err != nil {
		tb.Fatalf("failed to create translator: %v", err)
	}

	svc := NewService(logger, cfg, store, store, store, orClient, translator)
	svc.SetPeopleRepository(store)

	// Create mock archivist agent
	archivistAgent := new(agenttesting.MockAgent)
	svc.SetArchivistAgent(archivistAgent)

	return &testServices{
		Service:    svc,
		Store:      store,
		ORClient:   orClient,
		Agent:      archivistAgent,
		Logger:     logger,
		Cfg:        cfg,
		Translator: translator,
	}
}
