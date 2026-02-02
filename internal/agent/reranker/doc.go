// Package reranker implements agentic RAG candidate filtering using Flash LLM.
//
// The reranker receives top-N vector search candidates and intelligently
// selects the most relevant ones for the current query context.
//
// Architecture:
//
//	Input: 50 candidates (topics + people + artifacts)
//	  ↓
//	Turn 1: Flash sees summaries (~3K tokens), calls get_topics_content([ids])
//	  ↓
//	Turn 2: Flash receives full content of requested topics
//	  ↓
//	Output: Selected IDs with reasons (JSON response)
//
// Key Features:
//   - Agentic: Flash decides what to examine via tool calls
//   - Multimodal: Supports images/audio in query for better filtering
//   - Fallback: Returns vector top-N on timeout/error
//   - Reasons: Each selection includes explanation
//
// Result Format:
//
//	{"topics": [{"id": 42, "reason": "..."}, ...], "people": [...], "artifacts": [...]}
//
// Supported Candidate Types:
//   - Topic: Conversation summaries with message counts
//   - Person: Social graph entries with similarity scores
//   - Artifact: File metadata (images, PDFs, voice)
//
// Configuration:
//   - Agents.Reranker.Enabled: Enable/disable reranker
//   - Agents.Reranker.Model: LLM model (default: gemini-3-flash-preview)
//   - Agents.Reranker.Candidates: Max candidates to show (default 50)
//   - Agents.Reranker.Timeout: Total timeout for reranker flow
//   - Agents.Reranker.MaxTopics: Max topics in selection (default 15)
//   - Agents.Reranker.MaxPeople: Max people in selection (default 10)
//   - Agents.Reranker.Artifacts.Max: Max artifacts in selection (default 10)
//
// Fallback Strategy:
//  1. If Flash returns valid JSON → use it
//  2. If Flash made tool calls but invalid JSON → use requested IDs (top-5)
//  3. If timeout before tools → use vector top-5
//  4. If API error → use vector top-5
//
// Thread Safety:
//   - Reranker.Execute() is thread-safe (no shared state)
//   - Each call creates fresh request/response
package reranker
