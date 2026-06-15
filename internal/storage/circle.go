package storage

// Circle classifies a person's relationship to the memory scope.
//
// In a DM scope the circle is the person's relationship to the user (the bot
// owner). In a channel scope it marks insider vs outsider relative to the
// channel's participants: Work_Inner is a channel member (someone who posts in
// the channel), Other is an external person the participants merely mention.
const (
	CircleFamily    = "Family"
	CircleFriends   = "Friends"
	CircleWorkInner = "Work_Inner"
	CircleWorkOuter = "Work_Outer"
	CircleOther     = "Other"
)

// validCircles is the set of recognized circle values.
var validCircles = map[string]struct{}{
	CircleFamily:    {},
	CircleFriends:   {},
	CircleWorkInner: {},
	CircleWorkOuter: {},
	CircleOther:     {},
}

// NormalizeCircle returns c when it is a recognized circle value, otherwise
// CircleOther. It guards the write paths against empty or arbitrary values
// coming from LLM output so the stored taxonomy stays within the known set.
func NormalizeCircle(c string) string {
	if _, ok := validCircles[c]; ok {
		return c
	}
	return CircleOther
}
