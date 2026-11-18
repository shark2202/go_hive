package integrations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const batch2YahooChart = `{"chart":{"result":[{
	"meta":{"currency":"USD","symbol":"AAPL","exchangeName":"NMS","shortName":"Apple Inc.","regularMarketPrice":190.5,"chartPreviousClose":189.0,"regularMarketDayHigh":191.2,"regularMarketDayLow":188.4,"regularMarketVolume":51234567},
	"timestamp":[1700000000,1700086400],
	"indicators":{"quote":[{"open":[189.1,190.2],"high":[191.0,191.5],"low":[188.0,189.5],"close":[190.5,191.0],"volume":[1000,2000]}]}
}],"error":null}}`

const batch2YahooSummary = `{"quoteSummary":{"result":[{
	"price":{"shortName":"Apple Inc.","longName":"Apple Inc.","marketCap":{"raw":2950000000000,"fmt":"2.95T"},"regularMarketOpen":{"raw":189.1},"currency":"USD","exchangeName":"NMS"},
	"summaryDetail":{"trailingPE":{"raw":29.5},"dividendYield":{"raw":0.0055},"fiftyTwoWeekHigh":{"raw":199.0},"fiftyTwoWeekLow":{"raw":124.0}},
	"defaultKeyStatistics":{"trailingEps":{"raw":6.13}},
	"assetProfile":{"sector":"Technology","industry":"Consumer Electronics","longBusinessSummary":"Apple designs and sells devices.","website":"https://apple.com","fullTimeEmployees":164000,"country":"United States","city":"Cupertino","address1":"One Apple Park Way"},
	"incomeStatementHistory":{"statements":[{"maxAge":1,"endDate":{"raw":1700000000,"fmt":"2023-11-15"},"totalRevenue":{"raw":383285000000,"fmt":"383.29B"},"netIncome":{"raw":96995000000,"fmt":"97B"}}]}
}],"error":null}}`

const batch2YahooSearch = `{"quotes":[{"symbol":"AAPL","shortname":"Apple Inc.","exchange":"NMS","quoteType":"EQUITY"}],"news":[]}`

func batch2YahooServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v8/finance/chart/"):
			_, _ = w.Write([]byte(batch2YahooChart))
		case strings.HasPrefix(r.URL.Path, "/v10/finance/quoteSummary/"):
			_, _ = w.Write([]byte(batch2YahooSummary))
		case r.URL.Path == "/v1/finance/search":
			_, _ = w.Write([]byte(batch2YahooSearch))
		default:
			http.NotFound(w, r)
		}
	}))
}

// Parity: tools/tests/tools/test_yahoo_finance_tool.py::TestYahooFinanceQuote
func TestBatch2YahooFinanceQuote(t *testing.T) {
	server := batch2YahooServer(t)
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "yahoo_finance", map[string]any{
		"action": "yahoo_finance_quote",
		"symbol": "aapl",
	})
	if result["symbol"] != "AAPL" || result["price"] != 190.5 {
		t.Fatalf("result = %v", result)
	}
	if result["previous_close"] != 189.0 || result["name"] != "Apple Inc." {
		t.Fatalf("result = %v", result)
	}
	if result["market_cap"] != float64(2950000000000) || result["pe_ratio"] != 29.5 || result["eps"] != 6.13 {
		t.Fatalf("stats = %v", result)
	}
	if result["52w_high"] != 199.0 || result["currency"] != "USD" || result["exchange"] != "NMS" {
		t.Fatalf("result = %v", result)
	}

	missing := batch2Execute(t, registry, "yahoo_finance", map[string]any{"action": "yahoo_finance_quote"})
	if missing["error"] != "symbol is required" {
		t.Fatalf("missing = %v", missing)
	}
}

// Parity: tools/tests/tools/test_yahoo_finance_tool.py::TestYahooFinanceHistory
func TestBatch2YahooFinanceHistory(t *testing.T) {
	server := batch2YahooServer(t)
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "yahoo_finance", map[string]any{
		"action":   "yahoo_finance_history",
		"symbol":   "AAPL",
		"period":   "5d",
		"interval": "1d",
	})
	points, _ := result["data"].([]map[string]any)
	if len(points) != 2 {
		t.Fatalf("points = %v", result["data"])
	}
	if points[0]["date"] != "2023-11-14" || points[1]["date"] != "2023-11-15" {
		t.Fatalf("dates = %v", points)
	}
	if points[0]["open"] != 189.1 || points[0]["close"] != 190.5 || points[1]["volume"] != 2000 {
		t.Fatalf("point = %v", points[0])
	}
	if result["period"] != "5d" || result["symbol"] != "AAPL" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: 上游测试类未覆盖 yahoo_finance_financials（仅 quote/history/info/search）。
func TestBatch2YahooFinanceFinancials(t *testing.T) {
	server := batch2YahooServer(t)
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "yahoo_finance", map[string]any{
		"action":    "yahoo_finance_financials",
		"symbol":    "AAPL",
		"statement": "income",
	})
	data, _ := result["data"].(map[string]any)
	period, _ := data["2023-11-15"].(map[string]any)
	if period["totalRevenue"] != float64(383285000000) || period["netIncome"] != float64(96995000000) {
		t.Fatalf("period = %v", period)
	}
	if _, present := period["maxAge"]; present {
		t.Fatalf("maxAge leaked into period: %v", period)
	}

	invalid := batch2Execute(t, registry, "yahoo_finance", map[string]any{
		"action": "yahoo_finance_financials", "symbol": "AAPL", "statement": "profit",
	})
	if !strings.Contains(invalid["error"].(string), "Invalid statement type") {
		t.Fatalf("invalid = %v", invalid)
	}
}

// Parity: tools/tests/tools/test_yahoo_finance_tool.py::TestYahooFinanceInfo
func TestBatch2YahooFinanceInfo(t *testing.T) {
	server := batch2YahooServer(t)
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "yahoo_finance", map[string]any{
		"action": "yahoo_finance_info",
		"symbol": "AAPL",
	})
	if result["sector"] != "Technology" || result["industry"] != "Consumer Electronics" {
		t.Fatalf("result = %v", result)
	}
	if result["employees"] != float64(164000) || result["address"] != "One Apple Park Way" {
		t.Fatalf("result = %v", result)
	}
	if result["description"] != "Apple designs and sells devices." {
		t.Fatalf("description = %v", result["description"])
	}
}

// Parity: tools/tests/tools/test_yahoo_finance_tool.py::TestYahooFinanceSearch
func TestBatch2YahooFinanceSearch(t *testing.T) {
	server := batch2YahooServer(t)
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "yahoo_finance", map[string]any{
		"action": "yahoo_finance_search",
		"query":  "apple",
	})
	items, _ := result["results"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", result["results"])
	}
	if items[0]["symbol"] != "AAPL" || items[0]["type"] != "EQUITY" || items[0]["exchange"] != "NMS" {
		t.Fatalf("item = %v", items[0])
	}
}

// batch2YouTubeServer serves a watch page with caption tracks and a json3
// timedtext payload, matching the wire format youtube-transcript-api reads.
func batch2YouTubeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/watch":
			if r.URL.Query().Get("v") != "dQw4w9WgXcQ" {
				t.Errorf("video = %q", r.URL.Query().Get("v"))
			}
			tracks, err := json.Marshal([]map[string]any{
				{
					"baseUrl":        base + "/api/timedtext?v=dQw4w9WgXcQ&lang=en",
					"name":           map[string]any{"simpleText": "English"},
					"languageCode":   "en",
					"isTranslatable": true,
				},
				{
					"baseUrl":        base + "/api/timedtext?v=dQw4w9WgXcQ&lang=de",
					"name":           map[string]any{"simpleText": "Deutsch"},
					"languageCode":   "de",
					"kind":           "asr",
					"isTranslatable": true,
				},
			})
			if err != nil {
				t.Fatalf("marshal tracks: %v", err)
			}
			page := `<html><script>var ytInitialPlayerResponse = {"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":` + string(tracks) + `}}};</script></html>`
			_, _ = w.Write([]byte(page))
		case "/api/timedtext":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"events":[{"tStartMs":0,"dDurationMs":1500,"segs":[{"utf8":"Hello"}]},{"tStartMs":1500,"dDurationMs":1000,"segs":[{"utf8":" <b>world</b>"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

// Parity: tools/tests/tools/test_youtube_transcript_tool.py::TestYoutubeGetTranscript
func TestBatch2YoutubeGetTranscript(t *testing.T) {
	server := batch2YouTubeServer(t)
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "youtube_transcript", map[string]any{
		"action":   "youtube_get_transcript",
		"video_id": "dQw4w9WgXcQ",
	})
	if result["language"] != "English" || result["language_code"] != "en" || result["is_generated"] != false {
		t.Fatalf("result = %v", result)
	}
	if result["snippet_count"] != 2 {
		t.Fatalf("snippet_count = %v", result["snippet_count"])
	}
	snippets, _ := result["snippets"].([]map[string]any)
	if len(snippets) != 2 {
		t.Fatalf("snippets = %v", result["snippets"])
	}
	if snippets[0]["text"] != "Hello" || snippets[0]["start"] != float64(0) || snippets[0]["duration"] != 1.5 {
		t.Fatalf("snippet = %v", snippets[0])
	}
	if snippets[1]["text"] != "world" {
		t.Fatalf("formatting not stripped: %v", snippets[1])
	}

	german := batch2Execute(t, registry, "youtube_transcript", map[string]any{
		"action": "youtube_get_transcript", "video_id": "dQw4w9WgXcQ", "language": "de",
	})
	if german["language_code"] != "de" || german["is_generated"] != true {
		t.Fatalf("german = %v", german)
	}

	missing := batch2Execute(t, registry, "youtube_transcript", map[string]any{"action": "youtube_get_transcript"})
	if missing["error"] != "video_id is required" {
		t.Fatalf("missing = %v", missing)
	}
}

// Parity: tools/tests/tools/test_youtube_transcript_tool.py::TestYoutubeListTranscripts
func TestBatch2YoutubeListTranscripts(t *testing.T) {
	server := batch2YouTubeServer(t)
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "youtube_transcript", map[string]any{
		"action":   "youtube_list_transcripts",
		"video_id": "dQw4w9WgXcQ",
	})
	if result["count"] != 2 {
		t.Fatalf("result = %v", result)
	}
	tracks, _ := result["transcripts"].([]map[string]any)
	if len(tracks) != 2 {
		t.Fatalf("tracks = %v", result["transcripts"])
	}
	if tracks[0]["language"] != "English" || tracks[1]["is_generated"] != true {
		t.Fatalf("tracks = %v", tracks)
	}
}
