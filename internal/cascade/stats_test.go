package cascade

import "testing"

func TestParseStatsAcceptsStringAndNumberValues(t *testing.T) {
	for name, raw := range map[string]string{
		"xray<26 string": `{"stat":[{"name":"user>>>a@x>>>traffic>>>downlink","value":"100"},{"name":"user>>>a@x>>>traffic>>>uplink","value":"7"}]}`,
		"xray26 number":  `{"stat":[{"name":"user>>>a@x>>>traffic>>>downlink","value":100},{"name":"user>>>a@x>>>traffic>>>uplink","value":7},{"name":"user>>>a@x>>>online","value":2}]}`,
	} {
		got, err := parseStats([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c := got["a@x"]; c == nil || c.Rx != 100 || c.Tx != 7 || len(got) != 1 {
			t.Fatalf("%s: got %+v", name, got)
		}
	}
}
