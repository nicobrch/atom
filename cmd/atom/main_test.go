package main

import "testing"

func TestCommandMatchesFiltersByTypedPrefix(t *testing.T) {
	got := commandMatches("/m")
	if len(got) != 2 || got[0] != "/model <id>" || got[1] != "/models" {
		t.Fatalf("/m matches %q", got)
	}
}
