package main

import (
	"slices"
	"testing"

	"ircc/internal/rooms"
)

// TestPendingInvites: only a wanted nick that is awake and not yet a member is invited.
func TestPendingInvites(t *testing.T) {
	ends := []rooms.Endpoint{
		{Member: rooms.Member{Nick: "alpha"}},
		{Member: rooms.Member{Nick: "beta"}},
		{Member: rooms.Member{Nick: "other"}},
	}
	members := []rooms.Member{{Nick: "beta"}}
	got := pendingInvites([]string{"alpha", "beta", "ghost"}, ends, members)
	// alpha: wanted, awake, new -> invite. beta: already a member. ghost: not awake. other: not wanted.
	if !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("pendingInvites = %q, want [alpha]", got)
	}
	if got := pendingInvites([]string{"ghost"}, ends, nil); len(got) != 0 {
		t.Fatalf("asleep nick invited: %q", got)
	}
	if got := pendingInvites(nil, ends, nil); len(got) != 0 {
		t.Fatalf("no -invite still invited: %q", got)
	}
}
