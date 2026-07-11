package storage

// FactKind records the provenance of a stored fact — how much epistemic
// weight it carries when re-injected into LLM context.
//
// The distinction exists because facts are fed back into every conversation
// as ground truth: a user's one-sided verdict about another person
// ("X is manipulative") stored as a plain fact becomes something the model
// cannot argue with. Tagging provenance lets the context layer present such
// facts as testimony rather than truth.
const (
	// FactKindSelfReport is the default: something the user stated about
	// themselves or their own life.
	FactKindSelfReport = "self_report"
	// FactKindUserOpinion is the user's judgment or interpretation of other
	// people and conflicts, known only from the user's side.
	FactKindUserOpinion = "user_opinion"
	// FactKindVerified is reserved for facts confirmed by evidence beyond the
	// user's account (documents, external records). Not assignable by agents.
	FactKindVerified = "verified"
	// FactKindConstraint is a user instruction that restricts the assistant's
	// behavior ("don't be harsh", "no psychological interpretations"). Stored
	// separately so the system prompt can treat it as a preference that may be
	// overridden in safety-relevant situations, not as an absolute rule.
	FactKindConstraint = "constraint"
)

// validFactKinds is the set of recognized fact kinds.
var validFactKinds = map[string]struct{}{
	FactKindSelfReport:  {},
	FactKindUserOpinion: {},
	FactKindVerified:    {},
	FactKindConstraint:  {},
}

// NormalizeFactKind returns k when it is a recognized kind, otherwise
// FactKindSelfReport. It guards the write paths against empty or arbitrary
// values coming from LLM output so the stored taxonomy stays within the
// known set.
func NormalizeFactKind(k string) string {
	if _, ok := validFactKinds[k]; ok {
		return k
	}
	return FactKindSelfReport
}
