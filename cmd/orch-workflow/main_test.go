package main

import (
	"reflect"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"alice", []string{"alice"}},
		{"alice,bob", []string{"alice", "bob"}},
		{" alice , bob ", []string{"alice", "bob"}},
		{"alice,,bob", []string{"alice", "bob"}},
		{",alice,", []string{"alice"}},
		{"   ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := splitCSV(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitCSV(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}
