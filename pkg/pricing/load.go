package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The public price lists.
const (
	LiteLLMURL    = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	OpenRouterURL = "https://openrouter.ai/api/v1/models"
	// How long a downloaded list is used before it is read again.
	MaxAge = time.Hour
	// OverridesFile, in the directory Load is given, holds a user's own
	// rates in USD per million tokens:
	// {"my-model": {"input": 3, "output": 15, "cacheWrite": 3.75, "cacheRead": 0.3}}
	OverridesFile = "pricing.json"
)

// Options say where the lists come from; zero values are the defaults.
type Options struct {
	// Dir holds the cached lists and OverridesFile.
	Dir                       string
	Client                    *http.Client
	LiteLLMURL, OpenRouterURL string
	// Offline reads only the cache.
	Offline bool
}

// Load is the table from lists at most MaxAge old: a list is downloaded
// again when older, and the last download is used when that fails. A list
// with neither is empty, so its models have no price; err says why.
func Load(ctx context.Context, o Options) (*Table, error) {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if o.LiteLLMURL == "" {
		o.LiteLLMURL = LiteLLMURL
	}
	if o.OpenRouterURL == "" {
		o.OpenRouterURL = OpenRouterURL
	}
	var errs []string
	var at time.Time // the oldest list's; zero with none
	get := func(name, url string, parse func([]byte) (map[string]Rates, error)) map[string]Rates {
		path := filepath.Join(o.Dir, "pricing-"+name+".json")
		body, mod, err := cached(path)
		if !o.Offline && (err != nil || time.Since(mod) > MaxAge) {
			if fresh, ferr := download(ctx, o.Client, url); ferr == nil {
				if _, perr := parse(fresh); perr == nil {
					body, mod, err = fresh, time.Now(), nil
					writeFile(path, fresh)
				} else {
					errs = append(errs, name+": "+perr.Error())
				}
			} else {
				errs = append(errs, name+": "+ferr.Error())
			}
		}
		if err != nil {
			return nil
		}
		if at.IsZero() || mod.Before(at) {
			at = mod
		}
		r, _ := parse(body)
		return r
	}
	lite := get("litellm", o.LiteLLMURL, parseLiteLLM)
	or := get("openrouter", o.OpenRouterURL, parseOpenRouter)
	over, oerr := readOverrides(filepath.Join(o.Dir, OverridesFile))
	if oerr != nil {
		errs = append(errs, OverridesFile+": "+oerr.Error())
	}
	t := NewTable(lite, or, over)
	t.At = at
	if len(errs) > 0 {
		return t, fmt.Errorf("pricing: %s", strings.Join(errs, "; "))
	}
	return t, nil
}

func cached(path string) ([]byte, time.Time, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := os.ReadFile(path)
	return b, st.ModTime(), err
}

func download(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}

// writeFile replaces path whole, so a reader never sees half a list.
func writeFile(path string, b []byte) {
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, path)
	}
}

func valid(v float64) bool { return v >= 0 && v < 1 } // a negative is "varies" (a router); ≥ $1 a token is a typo

func parseLiteLLM(b []byte) (map[string]Rates, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := map[string]Rates{}
	for k, v := range raw {
		var e struct {
			In  float64 `json:"input_cost_per_token"`
			Out float64 `json:"output_cost_per_token"`
			CW  float64 `json:"cache_creation_input_token_cost"`
			CR  float64 `json:"cache_read_input_token_cost"`
		}
		if k == "sample_spec" || json.Unmarshal(v, &e) != nil || !valid(e.In) || !valid(e.Out) || !valid(e.CW) || !valid(e.CR) {
			continue
		}
		out[k] = Rates{e.In, e.Out, e.CW, e.CR}
	}
	return out, nil
}

func parseOpenRouter(b []byte) (map[string]Rates, error) {
	var raw struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				CacheRead  string `json:"input_cache_read"`
				CacheWrite string `json:"input_cache_write"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if raw.Data == nil {
		return nil, fmt.Errorf("no data")
	}
	num := func(s string) (float64, bool) {
		if s == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil && valid(f)
	}
	out := map[string]Rates{}
	for _, m := range raw.Data {
		in, ok1 := num(m.Pricing.Prompt)
		o, ok2 := num(m.Pricing.Completion)
		cr, ok3 := num(m.Pricing.CacheRead)
		cw, ok4 := num(m.Pricing.CacheWrite)
		if m.ID != "" && ok1 && ok2 && ok3 && ok4 {
			out[m.ID] = Rates{in, o, cw, cr}
		}
	}
	return out, nil
}

// readOverrides is OverridesFile, per million tokens, as rates per token.
// No file is no overrides.
func readOverrides(path string) (map[string]Rates, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]Rates
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, r := range m {
		m[k] = Rates{r.Input / 1e6, r.Output / 1e6, r.CacheWrite / 1e6, r.CacheRead / 1e6}
	}
	return m, nil
}
