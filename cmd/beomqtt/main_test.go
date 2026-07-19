package main

import "testing"

func TestSerialFromJID(t *testing.T) {
	tests := []struct{ jid, want string }{
		{"1111.2222222.12345678@products.bang-olufsen.com", "12345678"},
		{"1111.2222222.99999999@products.bang-olufsen.com", "99999999"},
		{"no-dots-or-at", "no-dots-or-at"},
		{"UPPER.CASE.ABC@x", "abc"},
	}
	for _, tt := range tests {
		if got := serialFromJID(tt.jid); got != tt.want {
			t.Errorf("serialFromJID(%q) = %q, want %q", tt.jid, got, tt.want)
		}
	}
}
