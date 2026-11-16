// Package pricing prices tokens at public API rates: LiteLLM's price list
// first, OpenRouter's second, a user's overrides over both.
//
// The price is what the same tokens would cost on the API. Most coding
// agents run on a subscription, which does not pay per token, so callers
// say "API-equivalent", never "spent". A model no list knows has no price
// (ok false), never 0.
//
// The lookup order and its helpers are ported from tokscale
// (crates/tokscale-core/src/pricing/lookup.rs), MIT, Copyright (c) 2025
// Junho Yeo; see NOTICE. Its request tiers (above 128k/200k/272k tokens),
// the models.dev source and the provider hints are left out.
package pricing

import (
	"sort"
	"strings"
	"time"
)

// Rates are USD per token.
type Rates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheWrite float64 `json:"cacheWrite"`
	CacheRead  float64 `json:"cacheRead"`
}

func (r Rates) usable() bool { return r.Input > 0 || r.Output > 0 }

// Cost is the tokens at these rates, in USD.
func (r Rates) Cost(input, output, cacheWrite, cacheRead int64) float64 {
	return float64(input)*r.Input + float64(output)*r.Output + float64(cacheWrite)*r.CacheWrite + float64(cacheRead)*r.CacheRead
}

// Match is a model's rates and where they came from.
type Match struct {
	Rates
	// Source is "override", "litellm" or "openrouter"; Key the list's name
	// for the model; How the step that found it: exact, alias, normalized,
	// prefix or fuzzy.
	Source, Key, How string
}

// Table is the price lists, by lower-cased key.
type Table struct {
	// At is when the lists were read from the network.
	At        time.Time
	Overrides map[string]Rates
	lists     [2]list
}

type list struct {
	name  string
	rates map[string]Rates
	keys  []string // sorted, for a fuzzy pass that gives the same answer every time
}

// NewTable is a table of the two lists, keys as the lists spell them.
func NewTable(litellm, openrouter map[string]Rates, overrides map[string]Rates) *Table {
	t := &Table{Overrides: map[string]Rates{}}
	for k, r := range overrides {
		t.Overrides[strings.ToLower(k)] = r
	}
	for i, src := range []map[string]Rates{litellm, openrouter} {
		l := list{name: [2]string{"litellm", "openrouter"}[i], rates: map[string]Rates{}}
		for k, r := range src {
			k = strings.ToLower(k)
			if !r.usable() || (i == 0 && strings.HasPrefix(k, "github_copilot/")) {
				continue // Copilot's own rows are its subscription, $0
			}
			l.rates[k] = r
			l.keys = append(l.keys, k)
		}
		sort.Strings(l.keys)
		t.lists[i] = l
	}
	return t
}

var (
	providerPrefixes = []string{"openai/", "anthropic/", "google/", "meta-llama/", "mistralai/", "minimax/", "deepseek/", "qwen/", "cohere/", "perplexity/", "x-ai/"}
	resellers        = []string{"azure/", "azure_ai/", "bedrock/", "vertex_ai/", "together/", "together_ai/", "fireworks_ai/", "groq/", "openrouter/", "orcarouter/"}
	fuzzyBlocklist   = map[string]bool{"auto": true, "mini": true, "chat": true, "base": true, "claude": true, "anthropic": true, "gemini": true, "model": true, "router": true, "default": true}
	claudeFamilies   = []string{"opus", "sonnet", "haiku", "fable"}
)

// Lookup finds model's rates: an override, then exact, alias, normalized
// version, provider prefix, a stripped suffix, fuzzy.
func (t *Table) Lookup(model string) (Match, bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(id, '['); i > 0 && strings.HasSuffix(id, "]") {
		id = id[:i] // claude-opus-4-6[1m]: the context size, not another model
	}
	if id == "" {
		return Match{}, false
	}
	if r, ok := t.Overrides[id]; ok {
		return Match{Rates: r, Source: "override", Key: id, How: "exact"}, true
	}
	if m, ok := t.direct(id, "exact"); ok {
		return m, true
	}
	if s, ok := stripKnownPrefix(id); ok {
		if m, ok := t.direct(s, "prefix"); ok {
			return m, true
		}
	}
	for _, p := range providerPrefixes {
		if m, ok := t.exact(p+id, "prefix"); ok {
			return m, true
		}
	}
	if i := strings.LastIndexByte(id, '/'); i >= 0 && i < len(id)-1 {
		if m, ok := t.direct(id[i+1:], "prefix"); ok {
			return m, true
		}
	}
	// Past here every candidate is a name nobody published: a Claude id
	// must not land on another family or version (tokscale's never-degrade).
	safe := func(m Match) bool { return !unsafeClaude(id, m.Key) }
	parts := strings.Split(id, "-")
	for n := 1; n <= 4 && n < len(parts); n++ {
		cand := strings.Join(parts[:len(parts)-n], "-")
		if len(cand) < 2 || isVersion(parts[len(parts)-n]) && n == 1 && claudeFamily(id) != "" {
			continue
		}
		if m, ok := t.direct(cand, "fuzzy"); ok && safe(m) {
			return m, true
		}
	}
	if len(id) >= 5 && !fuzzyBlocklist[id] {
		if m, ok := t.fuzzy(id); ok && safe(m) {
			return m, true
		}
	}
	return Match{}, false
}

// direct is an exact hit on id, its alias, or its normalized version.
func (t *Table) direct(id, how string) (Match, bool) {
	if m, ok := t.exact(id, how); ok {
		return m, true
	}
	if a, ok := aliases[id]; ok {
		if m, ok := t.exact(strings.ToLower(a), "alias"); ok {
			return m, true
		}
	}
	for _, p := range []string{"kimi-code/", "qoder/", "qoder-cn/"} {
		if a, ok := aliases[strings.TrimPrefix(id, p)]; ok && strings.HasPrefix(id, p) {
			if m, ok := t.exact(strings.ToLower(a), "alias"); ok {
				return m, true
			}
		}
	}
	if n, ok := normalizeClaude(id); ok && n != id {
		if m, ok := t.exact(n, "normalized"); ok {
			return m, true
		}
	}
	if n, ok := versionDots(id); ok {
		if m, ok := t.exact(n, "normalized"); ok {
			return m, true
		}
	}
	return Match{}, false
}

func (t *Table) exact(key, how string) (Match, bool) {
	for _, l := range t.lists {
		if r, ok := l.rates[key]; ok {
			return Match{Rates: r, Source: l.name, Key: key, How: how}, true
		}
	}
	return Match{}, false
}

// fuzzy is a key that holds id as a whole word, in id's family when one
// does; the shortest key from a model's own provider wins over a reseller's.
func (t *Table) fuzzy(id string) (Match, bool) {
	family := modelFamily(id)
	for _, inFamily := range []bool{true, false} {
		for _, l := range t.lists {
			var hits []string
			for _, k := range l.keys {
				if (!inFamily || strings.Contains(k, family)) && containsWord(k, id) {
					hits = append(hits, k)
				}
			}
			if len(hits) == 0 {
				continue
			}
			sort.SliceStable(hits, func(i, j int) bool {
				ri, rj := isReseller(hits[i]), isReseller(hits[j])
				if ri != rj {
					return !ri
				}
				return len(hits[i]) < len(hits[j])
			})
			return Match{Rates: l.rates[hits[0]], Source: l.name, Key: hits[0], How: "fuzzy"}, true
		}
	}
	return Match{}, false
}

func isReseller(k string) bool {
	for _, p := range resellers {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func stripKnownPrefix(id string) (string, bool) {
	for _, p := range providerPrefixes {
		if s, ok := strings.CutPrefix(id, p); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// modelFamily is tokscale's extract_model_family.
func modelFamily(id string) string {
	for _, f := range []string{"gpt-5", "gpt-4.1", "gpt-4o", "gpt-4", "o3", "o4", "opus", "sonnet", "haiku", "claude", "gemini-3", "gemini-2.5", "gemini-2", "gemini", "llama", "mistral", "deepseek", "qwen"} {
		if strings.Contains(id, f) {
			return f
		}
	}
	first, _, _ := strings.Cut(strings.NewReplacer("_", "-", ".", "-").Replace(id), "-")
	return first
}

func alnum(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// containsWord is whether s holds w with no letter or digit either side.
func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(w)
		if (j == 0 || !alnum(s[j-1])) && (end == len(s) || !alnum(s[end])) {
			return true
		}
		i = j + 1
	}
}

func claudeFamily(id string) string {
	for _, f := range claudeFamilies {
		if strings.Contains(id, f) {
			return f
		}
	}
	return ""
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r > 127 || !alnum(byte(r)) })
}

func isDigit1(s string, min byte) bool { return len(s) == 1 && s[0] >= min && s[0] <= '9' }

// normalizeClaude is the claude-<family>-<major>-<minor> key an id names in
// either order (claude-sonnet-4-6, opus-4.8, claude-4-6-sonnet), or
// claude-<family>-<major> for a bare modern major (claude-opus-5).
func normalizeClaude(id string) (string, bool) {
	if claudeFamily(id) == "" {
		return "", false
	}
	ws := words(id)
	isFam := func(s string) bool {
		for _, f := range claudeFamilies {
			if s == f {
				return true
			}
		}
		return false
	}
	for i := 0; i+2 < len(ws); i++ {
		if isFam(ws[i]) && isDigit1(ws[i+1], '4') && isDigit1(ws[i+2], '1') {
			return "claude-" + ws[i] + "-" + ws[i+1] + "-" + ws[i+2], true
		}
		if isDigit1(ws[i], '4') && isDigit1(ws[i+1], '1') && isFam(ws[i+2]) {
			return "claude-" + ws[i+2] + "-" + ws[i] + "-" + ws[i+1], true
		}
	}
	digits := func(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }
	for i, w := range ws {
		if !isFam(w) {
			continue
		}
		if i+1 < len(ws) && isDigit1(ws[i+1], '4') && (i+2 >= len(ws) || !digits(ws[i+2])) {
			return "claude-" + w + "-" + ws[i+1], true
		}
		if i >= 1 && isDigit1(ws[i-1], '4') && (i < 2 || !digits(ws[i-2])) {
			return "claude-" + w + "-" + ws[i-1], true
		}
	}
	return "", false
}

// unsafeClaude: a Claude id resolved to a key of another family, or of
// another version than the one it names.
func unsafeClaude(id, key string) bool {
	fam := claudeFamily(id)
	if fam == "" {
		return false
	}
	if kf := claudeFamily(key); kf != "" && kf != fam {
		return true
	}
	want, ok := normalizeClaude(id)
	if !ok {
		return false
	}
	got, ok := normalizeClaude(key)
	return ok && got != want
}

func isVersion(s string) bool { return s != "" && strings.Trim(s, "0123456789.") == "" }

// versionDots writes a-b between single digits as a.b (gpt-5-5 → gpt-5.5);
// a date (2025-05-14) keeps its dashes.
func versionDots(id string) (string, bool) {
	b := []byte(id)
	changed := false
	isD := func(i int) bool { return i >= 0 && i < len(b) && b[i] >= '0' && b[i] <= '9' }
	for i := 1; i < len(b)-1; i++ {
		if b[i] == '-' && isD(i-1) && isD(i+1) && !isD(i-2) && !isD(i+2) {
			b[i] = '.'
			changed = true
		}
	}
	return string(b), changed
}
