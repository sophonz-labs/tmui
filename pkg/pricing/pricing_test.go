package pricing

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const litellmFixture = `{
 "sample_spec": {"input_cost_per_token": 0.5},
 "claude-sonnet-4-5": {"input_cost_per_token": 3e-6, "output_cost_per_token": 15e-6, "cache_creation_input_token_cost": 3.75e-6, "cache_read_input_token_cost": 3e-7},
 "claude-opus-4-6": {"input_cost_per_token": 5e-6, "output_cost_per_token": 25e-6},
 "claude-opus-4-1": {"input_cost_per_token": 15e-6, "output_cost_per_token": 75e-6},
 "gpt-5.5": {"input_cost_per_token": 1.25e-6, "output_cost_per_token": 1e-5, "cache_read_input_token_cost": 1.25e-7},
 "gemini/gemini-2.5-pro": {"input_cost_per_token": 1.25e-6, "output_cost_per_token": 1e-5},
 "bedrock/gemini-2.5-pro-x": {"input_cost_per_token": 9e-6, "output_cost_per_token": 9e-5},
 "github_copilot/gpt-4o": {"input_cost_per_token": 0, "output_cost_per_token": 0},
 "dall-e-3": {"output_cost_per_image": 0.04}
}`

const openrouterFixture = `{"data": [
 {"id": "z-ai/glm-4.7", "pricing": {"prompt": "0.0000006", "completion": "0.0000022"}},
 {"id": "openrouter/auto", "pricing": {"prompt": "-1", "completion": "-1"}},
 {"id": "gpt-5.5", "pricing": {"prompt": "0.000009", "completion": "0.00009"}}
]}`

func fixture(t *testing.T) *Table {
	t.Helper()
	l, err := parseLiteLLM([]byte(litellmFixture))
	if err != nil {
		t.Fatal(err)
	}
	o, err := parseOpenRouter([]byte(openrouterFixture))
	if err != nil {
		t.Fatal(err)
	}
	return NewTable(l, o, map[string]Rates{"My-Model": {Input: 1e-6}})
}

func TestLookup(t *testing.T) {
	tb := fixture(t)
	for _, c := range []struct{ model, key, how string }{
		{"claude-sonnet-4-5", "claude-sonnet-4-5", "exact"},
		{"Claude-Sonnet-4-5", "claude-sonnet-4-5", "exact"},
		{"claude-opus-4-6[1m]", "claude-opus-4-6", "exact"},
		{"my-model", "my-model", "exact"},                        // override
		{"gpt-5.5", "gpt-5.5", "exact"},                          // LiteLLM before OpenRouter
		{"claude-opus-4.6-thinking", "claude-opus-4-6", "alias"}, // tokscale's table
		{"claude-4-6-opus", "claude-opus-4-6", "normalized"},     // either order
		{"gpt-5-5", "gpt-5.5", "normalized"},                     // version dots
		{"anthropic/claude-sonnet-4-5", "claude-sonnet-4-5", "normalized"},
		{"openai/gpt-5.5", "gpt-5.5", "prefix"},
		{"cx/gpt-5.5", "gpt-5.5", "prefix"},                               // any router's prefix
		{"glm-4.7", "z-ai/glm-4.7", "fuzzy"},                              // OpenRouter, by word
		{"big-pickle", "", ""},                                            // alias to glm-4.7, which is OpenRouter's z-ai/ only
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5", "normalized"}, // a dated snapshot
		{"gpt-5.5-codex-high", "gpt-5.5", "fuzzy"},                        // stripped suffixes
		{"gemini-2.5-pro", "gemini/gemini-2.5-pro", "fuzzy"},              // a vendor's key before a reseller's
		{"claude-opus-4-7", "", ""},                                       // never another version
		{"claude-opus-5", "", ""},
		{"claude-haiku-4-5", "", ""}, // never another family
		{"gpt-4o", "", ""},           // Copilot's $0 rows are not prices
		{"openrouter/auto", "", ""},  // nor a router's -1
		{"default", "", ""},
		{"", "", ""},
	} {
		m, ok := tb.Lookup(c.model)
		if c.key == "" {
			if ok {
				t.Errorf("%q: got %s (%s), want no price", c.model, m.Key, m.How)
			}
			continue
		}
		if !ok || m.Key != c.key || m.How != c.how {
			t.Errorf("%q: got %q %q %v, want %q %q", c.model, m.Key, m.How, ok, c.key, c.how)
		}
	}
	m, _ := tb.Lookup("claude-sonnet-4-5")
	if got := m.Cost(1e6, 1e6, 1e6, 1e6); math.Abs(got-(3+15+3.75+0.3)) > 1e-9 {
		t.Errorf("cost %v", got)
	}
}

func TestLoad(t *testing.T) {
	var hits atomic.Int32
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !up {
			http.Error(w, "down", 503)
			return
		}
		if r.URL.Path == "/litellm" {
			w.Write([]byte(litellmFixture))
		} else {
			w.Write([]byte(openrouterFixture))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	o := Options{Dir: dir, LiteLLMURL: srv.URL + "/litellm", OpenRouterURL: srv.URL + "/or"}
	os.WriteFile(filepath.Join(dir, OverridesFile), []byte(`{"mine": {"input": 2, "output": 8}}`), 0o600)

	tb, err := Load(context.Background(), o)
	if err != nil || hits.Load() != 2 {
		t.Fatalf("first load: %v, %d downloads", err, hits.Load())
	}
	if m, ok := tb.Lookup("mine"); !ok || m.Input != 2e-6 || m.Output != 8e-6 {
		t.Errorf("override per million: %+v", m)
	}
	if _, ok := tb.Lookup("glm-4.7"); !ok {
		t.Error("OpenRouter not read")
	}

	// Within the hour: the cache, no download.
	if _, err := Load(context.Background(), o); err != nil || hits.Load() != 2 {
		t.Fatalf("cached load: %v, %d downloads", err, hits.Load())
	}

	// Past the hour with the network down: the stale cache still prices, and err says so.
	old := time.Now().Add(-2 * time.Hour)
	for _, n := range []string{"pricing-litellm.json", "pricing-openrouter.json"} {
		os.Chtimes(filepath.Join(dir, n), old, old)
	}
	up = false
	tb, err = Load(context.Background(), o)
	if err == nil || hits.Load() != 4 {
		t.Fatalf("stale load: %v, %d downloads", err, hits.Load())
	}
	if _, ok := tb.Lookup("claude-sonnet-4-5"); !ok || time.Since(tb.At) < time.Hour {
		t.Errorf("stale cache not used, at %v", tb.At)
	}

	// Offline with no cache: no prices, no download.
	tb, _ = Load(context.Background(), Options{Dir: t.TempDir(), Offline: true, LiteLLMURL: srv.URL + "/litellm"})
	if _, ok := tb.Lookup("claude-sonnet-4-5"); ok || hits.Load() != 4 {
		t.Errorf("offline: %d downloads", hits.Load())
	}
}
