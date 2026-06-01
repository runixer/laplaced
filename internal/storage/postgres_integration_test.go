//go:build integration

package storage

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// Postgres integration tests. Skipped unless LAPLACED_TEST_PG_DSN is set:
//
//	LAPLACED_TEST_PG_DSN="host=<host> port=5432 dbname=<db> user=<user> \
//	  sslmode=require password=<password>" \
//	  go test -tags=integration ./internal/storage/...
//
// These run against a disposable database and DROP/recreate the schema.

func openTestPG(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("LAPLACED_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("LAPLACED_TEST_PG_DSN not set; skipping postgres integration test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open pgx: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping pgx: %v", err)
	}
	store := &Store{
		db:      db,
		dialect: postgresDialect{},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// Clean slate: drop every table this schema creates, then re-init.
	for _, tbl := range []string{
		"users", "history", "stats", "rag_logs", "topics", "memory_bank",
		"structured_facts", "fact_history", "reranker_logs", "agent_logs",
		"people", "artifacts", "scopes", "identities", "principals", "channels",
	} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + tbl + " CASCADE"); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
	if err := store.Init(); err != nil {
		t.Fatalf("init postgres schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store
}

// TestPostgresRoundTrip exercises the dialect-divergent paths end-to-end on a
// real Postgres: uuid scope columns, RETURNING id, BindTime, ON CONFLICT,
// date/bool/avg-age expressions, and pg_database_size.
func TestPostgresRoundTrip(t *testing.T) {
	store := openTestPG(t)
	ctx := context.Background()

	scope := PassthroughScopeID("mattermost", "alice@corp")

	if err := store.UpsertUser(User{ID: scope, Username: "alice", FirstName: "Alice"}); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	if err := store.AddMessageToHistory(scope, Message{UserID: scope, Role: "user", Content: "hello world", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("AddMessageToHistory: %v", err)
	}

	topicID, err := store.CreateTopic(Topic{UserID: scope, Summary: "greeting", StartMsgID: 1, EndMsgID: 1, SizeChars: 11, CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	if topicID == 0 {
		t.Fatal("CreateTopic returned id 0 (RETURNING id failed)")
	}

	factID, err := store.AddFact(Fact{UserID: scope, Relation: "likes", Category: "interest", Content: "photography", Type: "context", Importance: 50, CreatedAt: time.Now(), LastUpdated: time.Now()})
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if factID == 0 {
		t.Fatal("AddFact returned id 0")
	}

	if err := store.AddStat(Stat{UserID: scope, TokensUsed: 100, CostUSD: 0.01}); err != nil {
		t.Fatalf("AddStat: %v", err)
	}

	// Readbacks — must be non-empty and isolated to the uuid scope.
	facts, err := store.GetFacts(scope)
	if err != nil || len(facts) != 1 {
		t.Fatalf("GetFacts = %d facts, err=%v", len(facts), err)
	}
	if facts[0].UserID != scope {
		t.Fatalf("GetFacts scope round-trip mismatch: got %q want %q", facts[0].UserID, scope)
	}

	topics, err := store.GetTopics(scope)
	if err != nil || len(topics) != 1 {
		t.Fatalf("GetTopics = %d topics, err=%v", len(topics), err)
	}

	stats, err := store.GetStats()
	if err != nil || stats[scope].TokensUsed != 100 {
		t.Fatalf("GetStats = %+v, err=%v", stats[scope], err)
	}

	// Dashboard exercises date(), bool literals, AvgAge.
	dash, err := store.GetDashboardStats(scope)
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	if dash.TotalTopics != 1 || dash.TotalFacts != 1 || dash.TotalMessages != 1 {
		t.Fatalf("GetDashboardStats unexpected: %+v", dash)
	}

	// pg_database_size path.
	if sz, err := store.GetDBSize(); err != nil || sz <= 0 {
		t.Fatalf("GetDBSize = %d, err=%v", sz, err)
	}

	_ = ctx
}

// TestPostgresScopeResolution verifies IsChannelScope and the uuid scope round-trip
// on the postgres backend: a channel scope reports true, a passthrough DM id (no
// scopes row) reports false.
func TestPostgresScopeResolution(t *testing.T) {
	store := openTestPG(t)

	ch, err := store.GetOrCreateChannel("mattermost", "general", "#general")
	if err != nil {
		t.Fatalf("GetOrCreateChannel: %v", err)
	}
	dm := PassthroughScopeID("mattermost", "bob@corp")

	isCh, err := store.IsChannelScope(ch)
	if err != nil || !isCh {
		t.Fatalf("IsChannelScope(channel) = %v, err=%v", isCh, err)
	}
	isDM, err := store.IsChannelScope(dm)
	if err != nil || isDM {
		t.Fatalf("IsChannelScope(dm) = %v, err=%v", isDM, err)
	}
	// Deterministic channel scope.
	again, err := store.GetOrCreateChannel("mattermost", "general", "#general")
	if err != nil || again != ch {
		t.Fatalf("GetOrCreateChannel not deterministic: %q != %q (err=%v)", again, ch, err)
	}
}

// TestPostgresPrincipalModel exercises the normalized identity tables on real
// Postgres: uuid scope binding, the partial unique indexes on principals, the
// `excluded`/COALESCE upserts on channels, and the identity map. These SQL
// constructs are PG-specific risk that SQLite unit tests do not cover.
func TestPostgresPrincipalModel(t *testing.T) {
	store := openTestPG(t)

	// Principal dedup by ad_login across two handles (unified memory).
	sid, created, err := store.GetOrCreatePrincipal(PrincipalInput{
		ObjectGUID: "pg-guid-1", ADLogin: "k.gruzdev", Email: "k.gruzdev@corp", DisplayName: "K G",
	})
	if err != nil || !created {
		t.Fatalf("GetOrCreatePrincipal create: sid=%q created=%v err=%v", sid, created, err)
	}
	again, created, err := store.GetOrCreatePrincipal(PrincipalInput{ObjectGUID: "pg-guid-1"})
	if err != nil || created || again != sid {
		t.Fatalf("GetOrCreatePrincipal dedup: again=%q created=%v err=%v (want %q)", again, created, err, sid)
	}
	p, err := store.GetPrincipal(sid)
	if err != nil || p == nil || p.ADLogin != "k.gruzdev" {
		t.Fatalf("GetPrincipal: %+v err=%v", p, err)
	}

	// Two principals with NULL keys must coexist (partial unique index ignores NULL).
	a, _, err := store.GetOrCreatePrincipal(PrincipalInput{DisplayName: "Anon A"})
	if err != nil {
		t.Fatalf("anon a: %v", err)
	}
	b, _, err := store.GetOrCreatePrincipal(PrincipalInput{DisplayName: "Anon B"})
	if err != nil {
		t.Fatalf("anon b: %v", err)
	}
	if a == b {
		t.Fatalf("NULL-key principals collided: %q", a)
	}

	// Channel: deterministic scope, IsChannelScope true, display-name upsert.
	csid, err := store.GetOrCreateChannel("mattermost", "general", "#general")
	if err != nil || csid != PassthroughScopeID("mattermost", "general") {
		t.Fatalf("GetOrCreateChannel: csid=%q err=%v", csid, err)
	}
	if isCh, err := store.IsChannelScope(csid); err != nil || !isCh {
		t.Fatalf("IsChannelScope(channel) = %v err=%v", isCh, err)
	}
	if _, err := store.GetOrCreateChannel("mattermost", "general", ""); err != nil {
		t.Fatalf("channel re-upsert: %v", err)
	}
	ch, err := store.GetChannel(csid)
	if err != nil || ch == nil || ch.DisplayName != "#general" {
		t.Fatalf("GetChannel: %+v err=%v (empty name must not clobber)", ch, err)
	}

	// Identity map: two handles → one principal scope, re-read.
	if err := store.PutIdentity("mattermost", "uX", sid); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	idn, err := store.GetIdentity("mattermost", "uX")
	if err != nil || idn == nil || idn.ScopeID != sid {
		t.Fatalf("GetIdentity: %+v err=%v", idn, err)
	}
}

// TestPostgresUserIsolation mirrors the SQLite Test*UserIsolation suites on the
// Postgres backend: the partition-key filter (WHERE user_id = ?) must hold for
// uuid scopes too — no cross-scope read, by-id fetch, or delete may leak.
func TestPostgresUserIsolation(t *testing.T) {
	store := openTestPG(t)
	ctx := context.Background()

	s1 := PassthroughScopeID("mattermost", "user-one")
	s2 := PassthroughScopeID("mattermost", "user-two")

	f1, err := store.AddFact(Fact{UserID: s1, Relation: "is", Category: "private", Content: "s1 secret", Type: "identity", Importance: 100})
	if err != nil {
		t.Fatalf("AddFact s1: %v", err)
	}
	f2, err := store.AddFact(Fact{UserID: s2, Relation: "is", Category: "private", Content: "s2 secret", Type: "identity", Importance: 100})
	if err != nil {
		t.Fatalf("AddFact s2: %v", err)
	}
	tp1, err := store.CreateTopic(Topic{UserID: s1, Summary: "s1 topic", StartMsgID: 1, EndMsgID: 1})
	if err != nil {
		t.Fatalf("CreateTopic s1: %v", err)
	}
	if err := store.AddMessageToHistory(s1, Message{UserID: s1, Role: "user", Content: "s1 msg"}); err != nil {
		t.Fatalf("AddMessage s1: %v", err)
	}
	a1, err := store.AddArtifact(Artifact{UserID: s1, MessageID: 1, FileType: "image", FilePath: "/s1.jpg", ContentHash: "h-s1", State: "ready"})
	if err != nil {
		t.Fatalf("AddArtifact s1: %v", err)
	}

	// GetFacts is scoped.
	if facts, err := store.GetFacts(s1); err != nil || len(facts) != 1 || facts[0].Content != "s1 secret" {
		t.Fatalf("GetFacts(s1) leaked or missing: %+v err=%v", facts, err)
	}

	// By-id fetches cannot cross scopes.
	if facts, _ := store.GetFactsByIDs(s1, []int64{f2}); len(facts) != 0 {
		t.Errorf("GetFactsByIDs(s1, f2) leaked %d fact(s)", len(facts))
	}
	if tops, _ := store.GetTopicsByIDs(s2, []int64{tp1}); len(tops) != 0 {
		t.Errorf("GetTopicsByIDs(s2, tp1) leaked %d topic(s)", len(tops))
	}
	if arts, _ := store.GetArtifactsByIDs(s2, []int64{a1}); len(arts) != 0 {
		t.Errorf("GetArtifactsByIDs(s2, a1) leaked %d artifact(s)", len(arts))
	}

	// Delete cannot cross scopes: s2 deleting s1's fact must not remove it.
	_ = store.DeleteFact(s2, f1)
	if facts, _ := store.GetFacts(s1); len(facts) != 1 {
		t.Errorf("cross-scope DeleteFact removed s1's fact (have %d)", len(facts))
	}

	_ = ctx
}

// TestPostgresUnifiedMemory is the unified-memory payoff on Postgres: two transport
// handles of the same person resolve (via the principal) to one scope, so memory
// written under that scope is recalled regardless of which handle is used.
func TestPostgresUnifiedMemory(t *testing.T) {
	store := openTestPG(t)

	scope, created, err := store.GetOrCreatePrincipal(PrincipalInput{ADLogin: "k.gruzdev", DisplayName: "K G"})
	if err != nil || !created {
		t.Fatalf("GetOrCreatePrincipal: scope=%q created=%v err=%v", scope, created, err)
	}

	// Two handles (different transports) of the same person link to the scope.
	if err := store.PutIdentity("mattermost", "mm-26-char-id", scope); err != nil {
		t.Fatalf("PutIdentity time: %v", err)
	}
	if err := store.PutIdentity("telegram", "777", scope); err != nil {
		t.Fatalf("PutIdentity telegram: %v", err)
	}

	// Memory written once under the scope.
	if _, err := store.AddFact(Fact{UserID: scope, Relation: "likes", Category: "interest", Content: "astrophotography", Type: "context", Importance: 60}); err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	// Recall via EITHER handle resolves to the same scope and sees the fact.
	for _, h := range []struct{ transport, native string }{{"mattermost", "mm-26-char-id"}, {"telegram", "777"}} {
		idn, err := store.GetIdentity(h.transport, h.native)
		if err != nil || idn == nil {
			t.Fatalf("GetIdentity %s/%s: %+v err=%v", h.transport, h.native, idn, err)
		}
		if idn.ScopeID != scope {
			t.Fatalf("handle %s/%s resolved to %q, want shared scope %q", h.transport, h.native, idn.ScopeID, scope)
		}
		facts, err := store.GetFacts(idn.ScopeID)
		if err != nil || len(facts) != 1 || facts[0].Content != "astrophotography" {
			t.Fatalf("recall via %s/%s missing shared memory: %+v err=%v", h.transport, h.native, facts, err)
		}
	}
}

// TestPostgresEmbeddingShape guards the v0.7.0-class failure mode (empty RAG from
// a silent vector shape mismatch) on Postgres: an embedding stored as JSON-in-
// bytea must round-trip with its exact dimension and values, both via the scoped
// read and via the cross-user index loader the in-memory vector index uses.
func TestPostgresEmbeddingShape(t *testing.T) {
	store := openTestPG(t)
	scope := PassthroughScopeID("mattermost", "vec-user")

	emb := make([]float32, 1536)
	for i := range emb {
		emb[i] = float32(i%7)*0.013 - 0.04 // distinctive, non-trivial values
	}

	factID, err := store.AddFact(Fact{UserID: scope, Relation: "is", Category: "x", Content: "embedded fact", Type: "context", Importance: 10, Embedding: emb})
	if err != nil {
		t.Fatalf("AddFact with embedding: %v", err)
	}
	if _, err := store.CreateTopic(Topic{UserID: scope, Summary: "embedded topic", StartMsgID: 1, EndMsgID: 1, Embedding: emb}); err != nil {
		t.Fatalf("CreateTopic with embedding: %v", err)
	}

	assertEmb := func(label string, got []float32) {
		if len(got) != len(emb) {
			t.Fatalf("%s: embedding dim %d, want %d (shape mismatch — RAG would return empty)", label, len(got), len(emb))
		}
		for i := range emb {
			if got[i] != emb[i] {
				t.Fatalf("%s: embedding[%d] = %v, want %v (bytea round-trip corrupted)", label, i, got[i], emb[i])
			}
		}
	}

	// Scoped read path.
	facts, err := store.GetFacts(scope)
	if err != nil || len(facts) != 1 {
		t.Fatalf("GetFacts: %+v err=%v", facts, err)
	}
	assertEmb("GetFacts", facts[0].Embedding)

	// Cross-user index-loader path (what the in-memory vector index reads at startup).
	all, err := store.GetAllFacts()
	if err != nil {
		t.Fatalf("GetAllFacts: %v", err)
	}
	var found bool
	for _, f := range all {
		if f.ID == factID {
			assertEmb("GetAllFacts", f.Embedding)
			found = true
		}
	}
	if !found {
		t.Fatal("GetAllFacts did not return the embedded fact")
	}

	topics, err := store.GetTopics(scope)
	if err != nil || len(topics) != 1 {
		t.Fatalf("GetTopics: %+v err=%v", topics, err)
	}
	assertEmb("GetTopics", topics[0].Embedding)
}
