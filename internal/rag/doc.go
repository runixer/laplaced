// Package rag implements Retrieval-Augmented Generation for long-term memory.
//
// Data Flow:
//
//	Query → Enricher → Vector Search → Reranker → Context Assembly
//
//	1. Enricher (optional): Flash LLM expands query with context
//	2. Vector Search: Cosine similarity over topics/facts/people/artifacts embeddings
//	3. Reranker (optional): Flash LLM selects most relevant candidates (agentic)
//	4. Context Assembly: Loads full content for selected items
//
// Key Invariants:
//   - All vectors are user-isolated (map[userID][]vectors)
//   - Reranker is optional; fallback to vector top-N if disabled/error
//   - Background loop runs every minute for topic/fact extraction
//   - Vector store is in-memory; rebuilt from DB on startup
//
// Configuration:
//   - RAG.Enabled: Enable/disable RAG system
//   - RAG.MinSafetyThreshold: Minimum cosine similarity (default 0.1)
//   - RAG.RetrievedTopicsCount: Max topics without reranker (default 10)
//   - Agents.Reranker: Flash LLM filtering config
//   - Agents.Enricher: Query expansion config
//
// Vector Types:
//   - TopicVectorItem: Topic embeddings for conversation search
//   - FactVectorItem: Fact embeddings for profile facts
//   - PersonVectorItem: Person embeddings for social graph
//   - ArtifactVectorItem: File summary embeddings for artifact search
//
// Thread Safety:
//   - All vector searches use RLock (concurrent reads OK)
//   - Vector updates use Lock (blocks reads during update)
//   - Service methods are generally NOT thread-safe except where noted
//
// Main Entry Point:
//
//	Service.Retrieve() - Main RAG pipeline for message processing
package rag
