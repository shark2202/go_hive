package scrape

// Catalog definition for the web_scrape tool.

// scrapeDescription is the upstream web_scrape docstring, byte for byte:
// go_hive/contracts/tool-definitions.json, 1495 bytes, sha256
// a479a54a2eb01fe84a58028c23dcd3ba2cfe45ddd4d02f4ad1e3bf9511e04cc1.
const scrapeDescription = "Scrape and extract text content from a webpage.\n" +
	"\n" +
	"Uses a headless browser to render JavaScript and bypass bot detection.\n" +
	"Use when you need to read the content of a specific URL,\n" +
	"extract data from a website, or read articles/documentation.\n" +
	"\n" +
	"The extracted text body is **written to a file on disk** rather\n" +
	"than returned inline — JSON-wrapping multi-KB page text escapes\n" +
	"every newline and quote, which poisons the agent's context. The\n" +
	"response carries only metadata (title, description, headings,\n" +
	"structured data, links) plus ``saved_to`` pointing at the raw\n" +
	"text file. Use ``terminal_exec(\"cat <saved_to>\")`` (page large\n" +
	"output via ``terminal_output_get``) or ``terminal_rg`` on\n" +
	"``saved_to`` to inspect the body. Length is reported via\n" +
	"``total_length``.\n" +
	"\n" +
	"Args:\n" +
	"    url: URL of the webpage to scrape\n" +
	"    selector: CSS selector to target specific content (e.g., 'article', '.main-content')\n" +
	"    include_links: When True, links are inlined as `[text](url)` in\n" +
	"        the saved body and also returned as a `links` list\n" +
	"    respect_robots_txt: Whether to respect robots.txt rules (default False —\n" +
	"        operator has authorization for these one-off, low-volume fetches;\n" +
	"        pass True to re-enable the check for a given call)\n" +
	"\n" +
	"Returns:\n" +
	"    Dict with: url, final_url, title, description, page_type\n" +
	"    (article|listing|page), total_length, saved_to, headings,\n" +
	"    structured_data (json_ld + open_graph), and optionally links.\n" +
	"    On error, returns {\"error\": str, ...} with a hint when applicable."

var scrapeTool = &Tool{
	ID:          "web_scrape_tool",
	Name:        "web_scrape",
	DisplayName: "Web Scrape",
	Summary:     scrapeDescription,
	Actions: []Action{
		{
			Name:     "web_scrape",
			Summary:  scrapeDescription,
			Source:   "web_scrape_tool/web_scrape_tool.py",
			Required: []string{"url"},
			Params: map[string]any{
				"url": param("string"),
				// Python: selector: str | None = None → anyOf[string, null]
				// with an explicit null default.
				"selector": map[string]any{
					"anyOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "null"},
					},
					"default": nil,
				},
				"include_links":      paramDefault("boolean", false),
				"respect_robots_txt": paramDefault("boolean", false),
			},
		},
	},
}
