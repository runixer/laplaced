// Package memory handles fact extraction and people management.
//
// This package coordinates the Archivist agent (LLM-powered extraction)
// with storage repositories to maintain the user's long-term memory.
//
// Key Functions:
//   - ProcessSession: Extract facts from archived topic
//   - applyFactUpdates: Apply add/update/delete operations from Archivist
//   - applyPeopleUpdates: Manage social graph (delegates to people_ops.go)
//
// Archivist Integration:
//   - Archivist agent returns Result{Facts, People}
//   - This package applies changes to storage
//   - Embeddings created for new/updated items
//
// Data Flow:
//
//	Archived Topic → Archivist LLM → Result{Facts, People}
//	                  ↓
//	               Apply Updates
//	                  ↓
//	         Create Embeddings → Store in DB
//
// Fact Types:
//   - identity: Core user facts (name, preferences)
//   - importance: User-defined importance score (0-100)
//   - Facts with importance ≥ 90 always included in profile
//
// People System:
//   - People are organized in circles: Work_Inner, Work_Outer, Family, Other
//   - Merge operations deduplicate duplicate person records
//   - Each person has embedding for semantic search
//
// Thread Safety:
//   - Service methods are NOT thread-safe
//   - Caller must ensure single ProcessSession per topic at a time
//
// Configuration:
//   - Memory.MaxProfileFacts: Maximum facts in profile (default 50)
//   - Memory.ConsolidationThreshold: Similarity for fact dedup (default 0.85)
package memory
