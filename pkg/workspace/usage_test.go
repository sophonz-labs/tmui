package workspace

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// AI Usage names every window (a model's own by its label), says when it
// resets and how far use runs ahead of the time gone, and adds today's
// tokens, their API-rate cost and the last 30 days.
func TestUsageOverview(t *testing.T) {
	ws := panelWS(t)
	h := ws.host.(*fakeHost)
	now := time.Now()
	half := now.Add(84 * time.Hour).UnixMilli() // half a week left
	today, before := now.Format("2006-01-02"), now.AddDate(0, 0, -3).Format("2006-01-02")
	h.gets = map[string]string{
		"/api/usage": fmt.Sprintf(`{"agents":[{"agent":"claude","windows":[
			{"id":"seven_day","minutes":10080,"usedPercent":80,"resetsAt":%d},
			{"id":"seven_day_fable","label":"Fable","minutes":10080,"usedPercent":10,"resetsAt":%d}]}]}`, half, half),
		"/api/usage/history?days=30": fmt.Sprintf(`{"days":[
			{"date":%q,"input":1000,"output":500,"cacheWrite":0,"cacheRead":500,"cost":1.25},
			{"date":%q,"input":1000,"output":0,"cacheWrite":0,"cacheRead":0,"cost":null},
			{"date":%q,"input":4000,"output":0,"cacheWrite":0,"cacheRead":0,"cost":2}]}`, today, today, before),
	}
	var out string
	for range 3 {
		out = plain(strings.Join(ws.usageLines(60), "\n"))
		select {
		case f := <-ws.later:
			f()
		case <-time.After(200 * time.Millisecond):
		}
	}
	for _, want := range []string{"week 80%", "resets 3d", "+30", "Fable 10%", "-40", "today 3.0k tokens · $1.25 at API rates"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "30d                           █  ▆") {
		t.Errorf("30 days:\n%s", out)
	}
	// A host that keeps no history (tmui, stui): limits only, no error.
	delete(h.gets, "/api/usage/history?days=30")
	ws.panel.fetched["history"].at = time.Time{}
	ws.usageLines(60)
	(<-ws.later)()
	if out := plain(strings.Join(ws.usageLines(60), "\n")); strings.Contains(out, "Tokens") || !strings.Contains(out, "Fable") {
		t.Errorf("without history:\n%s", out)
	}
}
