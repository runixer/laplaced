package storage

import "testing"

func TestNormalizeCircle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"family", CircleFamily, CircleFamily},
		{"friends", CircleFriends, CircleFriends},
		{"work_inner", CircleWorkInner, CircleWorkInner},
		{"work_outer", CircleWorkOuter, CircleWorkOuter},
		{"other", CircleOther, CircleOther},
		{"empty falls back to Other", "", CircleOther},
		{"unknown falls back to Other", "Coworker", CircleOther},
		{"case-sensitive: lowercase is not valid", "work_inner", CircleOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeCircle(tt.in); got != tt.want {
				t.Errorf("NormalizeCircle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
