package integrations

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// arxivFeed is the Atom document returned by export.arxiv.org/api/query.
type arxivFeed struct {
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	ID        string `xml:"id"`
	Title     string `xml:"title"`
	Summary   string `xml:"summary"`
	Published string `xml:"published"`
	Authors   []struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
	Links []struct {
		Title string `xml:"title,attr"`
		Href  string `xml:"href,attr"`
	} `xml:"link"`
}

// arxivQueryURL builds the metadata query, mirroring arxiv.Search's parameters.
func arxivQueryURL(rt *Runtime, query string, idList []string, maxResults int, sortBy, sortOrder string) string {
	params := url.Values{}
	if len(idList) > 0 {
		params.Set("id_list", strings.Join(idList, ","))
	} else {
		params.Set("search_query", query)
	}
	params.Set("start", "0")
	params.Set("max_results", fmt.Sprint(maxResults))
	params.Set("sortBy", sortBy)
	params.Set("sortOrder", sortOrder)
	return batch2BaseURL(rt, "arxiv_tool", nil) + "/api/query?" + params.Encode()
}

func arxivSortBy(value string) string {
	switch value {
	case "lastUpdatedDate", "submittedDate", "relevance":
		return value
	default:
		return "relevance"
	}
}

func arxivSortOrder(value string) string {
	if value == "ascending" {
		return "ascending"
	}
	return "descending"
}

// arxivShortID strips the abstract URL prefix, like Result.get_short_id().
func arxivShortID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if index := strings.LastIndex(trimmed, "/abs/"); index >= 0 {
		return trimmed[index+len("/abs/"):]
	}
	return trimmed
}

func arxivFetch(ctx context.Context, rt *Runtime, target string) (*arxivFeed, map[string]any) {
	status, body, err := batch2Get(ctx, rt, target, map[string]string{"User-Agent": "Hive-Agent/1.0 (https://github.com/adenhq/hive)"})
	if err != nil {
		if ctx.Err() != nil {
			return nil, map[string]any{"success": false, "error": "Network unreachable."}
		}
		return nil, map[string]any{"success": false, "error": "Network unreachable."}
	}
	if status < 200 || status >= 300 {
		return nil, map[string]any{"success": false, "error": fmt.Sprintf("arXiv specific error: HTTP %d", status)}
	}
	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, map[string]any{"success": false, "error": "arXiv search failed: " + err.Error()}
	}
	return &feed, nil
}

func arxivSummaryText(raw string) string {
	return strings.ReplaceAll(strings.TrimSpace(raw), "\n", " ")
}

func arxivPublishedDate(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) >= 10 {
		return trimmed[:10]
	}
	return trimmed
}

func arxivSearchPapers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	idList := stringSliceArg(input, "id_list")
	if strings.TrimSpace(query) == "" && len(idList) == 0 {
		return map[string]any{
			"success": false,
			"error":   "Invalid Request: You must provide either a 'query' or an 'id_list'.",
		}, nil
	}
	maxResults := intArg(input, "max_results", 10)
	if maxResults > 100 {
		maxResults = 100
	}
	target := arxivQueryURL(rt, query, idList, maxResults,
		arxivSortBy(stringArg(input, "sort_by")), arxivSortOrder(stringArg(input, "sort_order")))
	feed, problem := arxivFetch(ctx, rt, target)
	if problem != nil {
		return problem, nil
	}
	results := make([]map[string]any, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		authors := make([]string, 0, len(entry.Authors))
		for _, author := range entry.Authors {
			authors = append(authors, strings.TrimSpace(author.Name))
		}
		categories := make([]string, 0, len(entry.Categories))
		for _, category := range entry.Categories {
			categories = append(categories, category.Term)
		}
		pdfURL := ""
		for _, link := range entry.Links {
			if link.Title == "pdf" {
				pdfURL = link.Href
				break
			}
		}
		results = append(results, map[string]any{
			"id":         arxivShortID(entry.ID),
			"title":      strings.Join(strings.Fields(entry.Title), " "),
			"summary":    arxivSummaryText(entry.Summary),
			"published":  arxivPublishedDate(entry.Published),
			"authors":    authors,
			"pdf_url":    pdfURL,
			"categories": categories,
		})
	}
	ids := idList
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{
		"success": true,
		"query":   query,
		"id_list": ids,
		"results": results,
		"total":   len(results),
	}, nil
}

// arxivCleanFilename mirrors the upstream regex cleanup for download names.
func arxivCleanFilename(value string, replacement string) string {
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || unicode.IsSpace(r) || r == '-' {
			builder.WriteRune(r)
		} else {
			builder.WriteString(replacement)
		}
	}
	return builder.String()
}

// arxivDownloadDir picks the managed directory for downloaded PDFs: a stable
// subdirectory of the first writable root (the Go equivalent of the Python
// module's process-lifetime TemporaryDirectory).
func arxivDownloadDir(rt *Runtime) (string, error) {
	roots := rt.Roots
	if len(roots) == 0 {
		roots = defaultRoots()
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		dir := filepath.Join(root, "arxiv_papers")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		return dir, nil
	}
	return os.MkdirTemp("", "arxiv_papers_")
}

func arxivDownloadPaper(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	paperID := strings.TrimSpace(stringArg(input, "paper_id"))
	if paperID == "" {
		return map[string]any{"success": false, "error": "paper_id is required"}, nil
	}
	target := arxivQueryURL(rt, "", []string{paperID}, 1, "relevance", "descending")
	feed, problem := arxivFetch(ctx, rt, target)
	if problem != nil {
		return problem, nil
	}
	if len(feed.Entries) == 0 {
		return map[string]any{"success": false, "error": fmt.Sprintf("No paper found with ID: %s", paperID)}, nil
	}
	entry := feed.Entries[0]
	pdfURL := ""
	for _, link := range entry.Links {
		if link.Title == "pdf" {
			pdfURL = link.Href
			break
		}
	}
	if pdfURL == "" {
		return map[string]any{"success": false, "error": "PDF URL not available for this paper."}, nil
	}
	// Upstream rewrites the download host to export.arxiv.org; use the
	// configured base host so the same rewrite works against a fake upstream.
	parsed, err := url.Parse(pdfURL)
	if err == nil {
		base, baseErr := url.Parse(batch2BaseURL(rt, "arxiv_tool", nil))
		if baseErr == nil && base.Host != "" {
			parsed.Scheme = base.Scheme
			parsed.Host = base.Host
			pdfURL = parsed.String()
		}
	}

	status, header, body, err := batch2GetWithHeaders(ctx, rt, pdfURL, map[string]string{"User-Agent": "Hive-Agent/1.0 (https://github.com/adenhq/hive)"})
	if err != nil {
		if ctx.Err() != nil {
			return map[string]any{"success": false, "error": "Network error: request timed out"}, nil
		}
		return map[string]any{"success": false, "error": "Failed during download or write: " + err.Error()}, nil
	}
	if status < 200 || status >= 300 {
		return map[string]any{"success": false, "error": fmt.Sprintf("Failed during download or write: HTTP %d", status)}, nil
	}
	contentType := header.Get("Content-Type")
	if contentType == "" && strings.HasPrefix(string(body), "%PDF") {
		contentType = "application/pdf"
	}
	if !strings.Contains(strings.ToLower(contentType), "pdf") {
		return map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Failed during download or write: Expected PDF content but got '%s'. arXiv may have returned an error page.", contentType),
		}, nil
	}

	title := strings.Join(strings.Fields(entry.Title), " ")
	cleanTitle := strings.TrimSpace(strings.ReplaceAll(arxivCleanFilename(title, ""), " ", "_"))
	if len(cleanTitle) > 50 {
		cleanTitle = cleanTitle[:50]
	}
	cleanID := arxivCleanFilename(paperID, "_")
	dir, err := arxivDownloadDir(rt)
	if err != nil {
		return map[string]any{"success": false, "error": "Failed during download or write: " + err.Error()}, nil
	}
	localPath := filepath.Join(dir, cleanTitle+"_"+cleanID+"_.pdf")
	if err := os.WriteFile(localPath, body, 0o644); err != nil {
		return map[string]any{"success": false, "error": "Failed during download or write: " + err.Error()}, nil
	}
	return map[string]any{"success": true, "file_path": localPath, "paper_id": paperID}, nil
}
