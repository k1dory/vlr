package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestAskBool(t *testing.T) {
	old := stdinReader
	t.Cleanup(func() { stdinReader = old })
	for _, tt := range []struct {
		input     string
		def, want bool
	}{
		{"нет\n", true, false}, {"да\n", false, true},
		{"\n", true, true}, {"\n", false, false},
		{"invalid\nno\n", true, false}, {"", false, false},
	} {
		stdinReader = bufio.NewReader(strings.NewReader(tt.input))
		if got := askBool("autostart", tt.def); got != tt.want {
			t.Errorf("input %q: got %t, want %t", tt.input, got, tt.want)
		}
	}
}
