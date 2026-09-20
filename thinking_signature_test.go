package main

import "testing"

func TestThinkingSignatureGolden(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "plain thinking",
			text: "think",
			want: "EiDcL8Gdj843bEdkHPFfgjoDrRDy3H2j9DIwVR9QcGkU9Q==",
		},
		{
			name: "unicode and newline",
			text: "thinking: 你好\nline 2",
			want: "EiD4PYGrZMKPsiK+MYH2CacmNZCV+Ts7TLg5nQzG9qWDCg==",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fakeThinkingSignature(test.text); got != test.want {
				t.Fatalf("fakeThinkingSignature(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}
