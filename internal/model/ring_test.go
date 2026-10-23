package model

import (
	"errors"
	"reflect"
	"testing"
)

func TestRing(t *testing.T) {
	tests := []struct {
		name string
		cap  int
		push []int
		want []int
	}{
		{"empty", 3, nil, []int{}},
		{"under cap", 3, []int{1, 2}, []int{1, 2}},
		{"exactly cap", 3, []int{1, 2, 3}, []int{1, 2, 3}},
		{"overflow drops oldest", 3, []int{1, 2, 3, 4, 5}, []int{3, 4, 5}},
		{"cap one", 1, []int{1, 2}, []int{2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewRing[int](tc.cap)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range tc.push {
				r.Push(v)
			}
			if got := r.All(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			if r.Len() != len(tc.want) {
				t.Fatalf("len %d", r.Len())
			}
		})
	}
}

func TestRingBadCap(t *testing.T) {
	for _, c := range []int{0, -1} {
		if _, err := NewRing[int](c); !errors.Is(err, ErrBadCap) {
			t.Fatalf("cap %d: got %v", c, err)
		}
	}
}

func TestRingZeroValuePush(t *testing.T) {
	var r Ring[int]
	r.Push(1)
	if r.Len() != 0 || len(r.All()) != 0 {
		t.Fatalf("zero ring held items: %v", r.All())
	}
}
