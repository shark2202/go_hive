package httpapi

import "testing"

func TestNormalizeMessagesURL(t *testing.T) {
	cases := map[string]string{
		"":                                      "",
		"https://api.anthropic.com":             "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/":            "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1":          "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1/messages": "https://api.anthropic.com/v1/messages",
		"https://proxy.example/anthropic":       "https://proxy.example/anthropic/v1/messages",
		"https://proxy.example/anthropic/":      "https://proxy.example/anthropic/v1/messages",
	}
	for raw, want := range cases {
		got, err := normalizeMessagesURL(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%q => %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeImagesURL(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"https://images.example":    "https://images.example/v1/images/generations",
		"https://images.example/":   "https://images.example/v1/images/generations",
		"https://images.example/v1": "https://images.example/v1/images/generations",
		"https://images.example/v1/images/generations":  "https://images.example/v1/images/generations",
		"https://images.example/v1/images/generations/": "https://images.example/v1/images/generations",
		"https://images.example/custom/generate":        "https://images.example/custom/generate",
	}
	for raw, want := range cases {
		got, err := normalizeImagesURL(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%q => %q, want %q", raw, got, want)
		}
	}
}
