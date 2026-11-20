package integrations

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const batch2ArxivAtom = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>http://arxiv.org/abs/1706.03762v5</id>
    <published>2017-06-12T17:57:34Z</published>
    <title>Attention Is All You Need</title>
    <summary>We propose a new network architecture.
It is based on attention.</summary>
    <author><name>Ashish Vaswani</name></author>
    <author><name>Noam Shazeer</name></author>
    <link title="pdf" href="http://export.arxiv.org/pdf/1706.03762v5"/>
    <category term="cs.CL"/>
    <category term="cs.LG"/>
  </entry>
</feed>`

// Parity: tools/tests/tools/test_arxiv_tool.py::TestSearchPapers
func TestBatch2ArxivSearchPapers(t *testing.T) {
	var seenQuery map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = map[string]string{}
		for key, values := range r.URL.Query() {
			seenQuery[key] = values[0]
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(batch2ArxivAtom))
	}))
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "arxiv", map[string]any{
		"action":      "search_papers",
		"query":       "ti:attention",
		"max_results": 5,
		"sort_by":     "submittedDate",
	})
	if result["success"] != true || result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	if seenQuery["search_query"] != "ti:attention" || seenQuery["max_results"] != "5" || seenQuery["sortBy"] != "submittedDate" {
		t.Fatalf("query = %v", seenQuery)
	}
	papers, _ := result["results"].([]map[string]any)
	if len(papers) != 1 {
		t.Fatalf("papers = %v", result["results"])
	}
	paper := papers[0]
	if paper["id"] != "1706.03762v5" || paper["published"] != "2017-06-12" {
		t.Fatalf("paper = %v", paper)
	}
	if strings.Contains(paper["summary"].(string), "\n") {
		t.Fatalf("summary not flattened: %q", paper["summary"])
	}
	authors, _ := paper["authors"].([]string)
	if len(authors) != 2 || authors[0] != "Ashish Vaswani" {
		t.Fatalf("authors = %v", paper["authors"])
	}
	categories, _ := paper["categories"].([]string)
	if len(categories) != 2 || categories[1] != "cs.LG" {
		t.Fatalf("categories = %v", paper["categories"])
	}
	if paper["pdf_url"] != "http://export.arxiv.org/pdf/1706.03762v5" {
		t.Fatalf("pdf_url = %v", paper["pdf_url"])
	}
}

// Parity: tools/tests/tools/test_arxiv_tool.py::TestSearchPapers
func TestBatch2ArxivSearchRequiresQueryOrIDs(t *testing.T) {
	registry := newBatch2Registry()
	result := batch2Execute(t, registry, "arxiv", map[string]any{"action": "search_papers"})
	if result["success"] != false {
		t.Fatalf("result = %v", result)
	}
	if !strings.Contains(result["error"].(string), "id_list") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_arxiv_tool.py::TestDownloadPaper
func TestBatch2ArxivDownloadPaper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/query" {
			_, _ = w.Write([]byte(batch2ArxivAtom))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/pdf/") {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4 fake paper body"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	root := t.TempDir()
	registry := batch2TestRegistry(t, server)
	registry.Runtime().Roots = []string{root}

	result := batch2Execute(t, registry, "arxiv", map[string]any{
		"action":   "download_paper",
		"paper_id": "1706.03762v5",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	path, _ := result["file_path"].(string)
	if !strings.HasPrefix(path, root) {
		t.Fatalf("file outside root: %q", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "%PDF-1.4 fake paper body" {
		t.Fatalf("body = %q", body)
	}
	if !strings.Contains(path, "Attention_Is_All_You_Need_1706_03762v5_.pdf") {
		t.Fatalf("filename = %q", path)
	}
}

// Parity: tools/tests/tools/test_duckduckgo_tool.py::TestDuckDuckGoSearch
func TestBatch2DuckDuckGoSearch(t *testing.T) {
	var form map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/html/" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		form = map[string]string{}
		for key := range r.PostForm {
			form[key] = r.PostForm.Get(key)
		}
		_, _ = w.Write([]byte(`<div class="result results_links">
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F&amp;rut=x">The Go <b>Programming</b> Language</a>
<a class="result__snippet" href="#">Go is an open source &amp; productive language.</a>
</div>`))
	}))
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "duckduckgo", map[string]any{
		"action":      "duckduckgo_search",
		"query":       "golang",
		"max_results": 3,
		"region":      "uk-en",
		"timelimit":   "w",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	items, _ := result["results"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", result["results"])
	}
	item := items[0]
	if item["title"] != "The Go Programming Language" {
		t.Fatalf("title = %v", item["title"])
	}
	if item["url"] != "https://go.dev/" {
		t.Fatalf("url = %v", item["url"])
	}
	if item["snippet"] != "Go is an open source & productive language." {
		t.Fatalf("snippet = %v", item["snippet"])
	}
	if form["q"] != "golang" || form["kl"] != "uk-en" || form["df"] != "w" {
		t.Fatalf("form = %v", form)
	}
}

// Parity: tools/tests/tools/test_duckduckgo_tool.py::TestDuckDuckGoNews
func TestBatch2DuckDuckGoNews(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html><script>DDG.deep.vqd = "4-1234567890123";</script></html>`))
		case "/news.js":
			if r.URL.Query().Get("vqd") != "4-1234567890123" {
				t.Errorf("vqd = %q", r.URL.Query().Get("vqd"))
			}
			_, _ = w.Write([]byte(`{"results":[{"title":"Release","url":"https://example.com/n","source":"Example","date":"2026-01-02","body":"news snippet"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "duckduckgo", map[string]any{
		"action": "duckduckgo_news",
		"query":  "release",
	})
	items, _ := result["results"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", result["results"])
	}
	if items[0]["source"] != "Example" || items[0]["snippet"] != "news snippet" || items[0]["date"] != "2026-01-02" {
		t.Fatalf("item = %v", items[0])
	}
}

// Parity: tools/tests/tools/test_duckduckgo_tool.py::TestDuckDuckGoImages
func TestBatch2DuckDuckGoImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`vqd="4-999"`))
		case "/i.js":
			_, _ = w.Write([]byte(`{"results":[{"title":"Gopher","image":"https://img.example/g.png","thumbnail":"https://img.example/t.png","url":"https://example.com","width":640,"height":480}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "duckduckgo", map[string]any{
		"action": "duckduckgo_images",
		"query":  "gopher",
		"size":   "Large",
	})
	items, _ := result["results"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", result["results"])
	}
	item := items[0]
	if item["image_url"] != "https://img.example/g.png" || item["thumbnail_url"] != "https://img.example/t.png" {
		t.Fatalf("item = %v", item)
	}
	if item["width"] != 640 || item["height"] != 480 {
		t.Fatalf("dimensions = %v", item)
	}
}

// Parity: tools/tests/tools/test_wikipedia_tool.py::test_search_wikipedia_success
func TestBatch2WikipediaSearch(t *testing.T) {
	var seenLimit, seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/w/rest.php/v1/search/page" {
			http.NotFound(w, r)
			return
		}
		seenLimit = r.URL.Query().Get("limit")
		seenQuery = r.URL.Query().Get("q")
		_, _ = w.Write([]byte(`{"pages":[
			{"title":"Go","key":"Go_(programming_language)","description":"Programming language","excerpt":"<span class=\"searchmatch\">Go</span> is a statically typed language."},
			{"title":"Gopher","key":"Gopher","excerpt":"A gopher is a rodent."}
		]}`))
	}))
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "search_wikipedia", map[string]any{
		"query":       "Go",
		"lang":        "en",
		"num_results": 20,
	})
	if result["count"] != 2 || result["lang"] != "en" {
		t.Fatalf("result = %v", result)
	}
	if seenLimit != "10" || seenQuery != "Go" {
		t.Fatalf("limit=%q query=%q", seenLimit, seenQuery)
	}
	pages, _ := result["results"].([]map[string]any)
	if len(pages) != 2 {
		t.Fatalf("pages = %v", result["results"])
	}
	if pages[0]["snippet"] != "Go is a statically typed language." {
		t.Fatalf("snippet = %v", pages[0]["snippet"])
	}
	if pages[0]["url"] != server.URL+"/wiki/Go_(programming_language)" {
		t.Fatalf("url = %v", pages[0]["url"])
	}
	if pages[1]["description"] != "No description available." {
		t.Fatalf("description = %v", pages[1]["description"])
	}
}

// Parity: tools/tests/tools/test_wikipedia_tool.py::test_search_wikipedia_empty_query
func TestBatch2WikipediaEmptyQuery(t *testing.T) {
	registry := newBatch2Registry()
	result := batch2Execute(t, registry, "search_wikipedia", map[string]any{"query": "  "})
	if result["error"] != "Query cannot be empty" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: 上游无 test_openmeteo_tool.py，冒烟覆盖两个 action 的形状。
func TestBatch2OpenMeteoCurrent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("current_weather") != "true" {
			t.Errorf("query = %v", r.URL.Query())
		}
		_, _ = w.Write([]byte(`{"latitude":52.52,"longitude":13.41,"current_weather":{"temperature":18.4,"windspeed":11.2,"winddirection":220,"weathercode":3,"is_day":1,"time":"2026-01-02T10:00"}}`))
	}))
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "openmeteo", map[string]any{
		"action":    "weather_get_current",
		"latitude":  52.52,
		"longitude": 13.41,
	})
	if result["temperature"] != 18.4 || result["weathercode"] != float64(3) {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: 上游无 test_openmeteo_tool.py，覆盖 forecast 字段映射与天数校验。
func TestBatch2OpenMeteoForecast(t *testing.T) {
	var days string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		days = r.URL.Query().Get("forecast_days")
		_, _ = w.Write([]byte(`{"daily":{"time":["2026-01-02","2026-01-03"],"temperature_2m_max":[9.1,10.2],"temperature_2m_min":[1.0,2.0],"precipitation_sum":[0,1.5],"weathercode":[3,61]}}`))
	}))
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "openmeteo", map[string]any{
		"action":    "weather_get_forecast",
		"latitude":  48.85,
		"longitude": 2.35,
		"days":      2,
	})
	dates, _ := result["dates"].([]any)
	if len(dates) != 2 || dates[1] != "2026-01-03" || days != "2" {
		t.Fatalf("result = %v days=%q", result, days)
	}
	precipitation, _ := result["precipitation"].([]any)
	if precipitation[1] != 1.5 {
		t.Fatalf("precipitation = %v", result["precipitation"])
	}

	invalid := batch2Execute(t, registry, "openmeteo", map[string]any{
		"action": "weather_get_forecast", "latitude": 1, "longitude": 2, "days": 30,
	})
	if invalid["error"] != "days must be between 1 and 16" {
		t.Fatalf("invalid = %v", invalid)
	}
}
