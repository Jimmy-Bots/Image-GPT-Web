package api

import "testing"

func TestSensitiveRuleMatches(t *testing.T) {
	tests := []struct {
		name string
		text string
		rule string
		want bool
	}{
		{name: "plain case insensitive", text: "Hello BAD Word here", rule: "bad word", want: true},
		{name: "plain no match", text: "hello world", rule: "blocked", want: false},
		{name: "wildcard star", text: "this has abcxxyz inside", rule: "abc*xyz", want: true},
		{name: "wildcard question", text: "foo a1c bar", rule: "a?c", want: true},
		{name: "regex prefix", text: "draw a 16 year old portrait", rule: `re:\b1[0-7]\s*year\s*old\b`, want: true},
		{name: "invalid regex ignored", text: "whatever", rule: "re:[", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sensitiveRuleMatches(tt.text, tt.rule)
			if got != tt.want {
				t.Fatalf("sensitiveRuleMatches(%q, %q) = %v, want %v", tt.text, tt.rule, got, tt.want)
			}
		})
	}
}

func TestFirstSensitiveWordReturnsMatchedRule(t *testing.T) {
	settings := map[string]any{
		"sensitive_words": []any{
			"simple banned",
			"foo*bar",
			`re:\bsecret\s+\d{4}\b`,
		},
	}

	if got := firstSensitiveWord("contains FOO-xx-BAR value", settings); got != "foo*bar" {
		t.Fatalf("unexpected wildcard match rule: %q", got)
	}
	if got := firstSensitiveWord("leak secret 1234 now", settings); got != `re:\bsecret\s+\d{4}\b` {
		t.Fatalf("unexpected regex match rule: %q", got)
	}
	if got := firstSensitiveWord("this is simple banned content", settings); got != "simple banned" {
		t.Fatalf("unexpected plain match rule: %q", got)
	}
}
