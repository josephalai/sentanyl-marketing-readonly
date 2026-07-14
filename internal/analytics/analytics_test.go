package analytics

import "testing"

// ANA-004: every registry metric must state definition, source, unit, and
// timezone — a metric without semantics is a number nobody can trust.
func TestRegistryComplete(t *testing.T) {
	if len(Registry) == 0 {
		t.Fatal("registry must not be empty")
	}
	seen := map[string]bool{}
	for _, m := range Registry {
		if m.Name == "" || m.Definition == "" || m.Source == "" || m.Unit == "" || m.Timezone == "" {
			t.Errorf("metric %q incomplete: %+v", m.Name, m)
		}
		if seen[m.Name] {
			t.Errorf("duplicate metric name %q", m.Name)
		}
		seen[m.Name] = true
	}
}

// ANA-007: bot heuristic — crawlers and empty UAs never create touches.
func TestLooksLikeBot(t *testing.T) {
	for _, ua := range []string{"", "Googlebot/2.1", "curl/8.0", "python-requests/2.31", "HeadlessChrome/119"} {
		if !LooksLikeBot(ua) {
			t.Errorf("%q must be classified as bot", ua)
		}
	}
	for _, ua := range []string{"Mozilla/5.0 (Macintosh; Intel Mac OS X) Safari/605.1", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0)"} {
		if LooksLikeBot(ua) {
			t.Errorf("%q must not be classified as bot", ua)
		}
	}
}
