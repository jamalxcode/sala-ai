package main

import "testing"

func TestToCredits(t *testing.T) {
	cases := []struct {
		name   string
		cost   float64
		markup float64
		want   int
	}{
		{"zero cost still charges 1", 0, 40, 1},
		{"tiny cost rounds up to 1", 0.0000001, 40, 1},
		// $1 * 0.307 KWD/$ * 1000 fils * 1.4 = 429.8 -> 430
		{"one dollar at 40%", 1, 40, 430},
		// $0.01 * 0.307 * 1000 = 3.07 -> 4
		{"no markup rounds up", 0.01, 0, 4},
		// exactly 307 fils must not become 308 through float error
		{"exact value not bumped", 1, 0, 307},
	}
	for _, c := range cases {
		if got := toCredits(c.cost, 0.307, c.markup); got != c.want {
			t.Errorf("%s: toCredits(%v, %v) = %d, want %d", c.name, c.cost, c.markup, got, c.want)
		}
	}
}

func TestModelCost(t *testing.T) {
	m := Model{InputUSD: 2, OutputUSD: 10}
	// 1,000 in + 500 out = 0.002 + 0.005 = $0.007
	if got := m.costUSD(1000, 500); got < 0.006999 || got > 0.007001 {
		t.Errorf("costUSD = %v, want 0.007", got)
	}
}

func TestEstimatePromptTokensOverestimates(t *testing.T) {
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hello there, how are you today?"}]}`)
	if got := estimatePromptTokens(body); got < 20 {
		t.Errorf("estimate %d looks too low for %d bytes", got, len(body))
	}
}

func TestStreamStateUsageAndChars(t *testing.T) {
	var s streamState
	lines := []string{
		": OPENROUTER PROCESSING\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n",
		"\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\",\"reasoning\":\"hmm\"}}]}\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"cost\":0.00042}}\n",
		"data: [DONE]\n",
		"data: not json\n",
	}
	for _, l := range lines {
		s.observe([]byte(l))
	}
	if s.chars != len("Hello")+len(" world")+len("hmm") {
		t.Errorf("chars = %d", s.chars)
	}
	if s.usage == nil || s.usage.PromptTokens != 12 || s.usage.CompletionTokens != 3 {
		t.Fatalf("usage = %+v", s.usage)
	}
	if s.usage.Cost == nil || *s.usage.Cost != 0.00042 {
		t.Errorf("cost = %v", s.usage.Cost)
	}
}

func TestStreamStateIgnoresEmptyUsage(t *testing.T) {
	var s streamState
	s.observe([]byte(`data: {"choices":[{"delta":{"content":"a"}}],"usage":null}`))
	s.observe([]byte(`data: {"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}`))
	if s.usage != nil {
		t.Errorf("expected no usage, got %+v", s.usage)
	}
}

func TestAppNameRe(t *testing.T) {
	for _, ok := range []string{"how.sala.company", "artograph", "app-1"} {
		if !appNameRe.MatchString(ok) {
			t.Errorf("%q should be allowed", ok)
		}
	}
	for _, bad := range []string{"", "Has Space", "-start", "a/b"} {
		if appNameRe.MatchString(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
