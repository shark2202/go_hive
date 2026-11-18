package integrations

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
)

// The Python module wraps youtube-transcript-api, which reads the watch page,
// extracts captionTracks and downloads the timedtext track. This port follows
// the same flow, with the base URL injectable for tests.

// youtubeJSONArray extracts a balanced JSON array that follows "key": in HTML.
func youtubeJSONArray(page, key string) ([]byte, bool) {
	marker := "\"" + key + "\":"
	index := strings.Index(page, marker)
	if index < 0 {
		return nil, false
	}
	start := index + len(marker)
	for start < len(page) && (page[start] == ' ' || page[start] == '\t' || page[start] == '\n' || page[start] == '\r') {
		start++
	}
	if start >= len(page) || page[start] != '[' {
		return nil, false
	}
	depth := 0
	inString := false
	escaped := false
	for cursor := start; cursor < len(page); cursor++ {
		char := page[cursor]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return []byte(page[start : cursor+1]), true
			}
		}
	}
	return nil, false
}

type youtubeCaptionTrack struct {
	BaseURL        string
	Language       string
	LanguageCode   string
	IsGenerated    bool
	IsTranslatable bool
}

func youtubeCaptionTracks(page string) []youtubeCaptionTrack {
	raw, ok := youtubeJSONArray(page, "captionTracks")
	if !ok {
		return nil
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	tracks := make([]youtubeCaptionTrack, 0, len(decoded))
	for _, item := range decoded {
		track := youtubeCaptionTrack{
			BaseURL:      stringArg(item, "baseUrl"),
			LanguageCode: stringArg(item, "languageCode"),
			IsGenerated:  stringArg(item, "kind") == "asr",
		}
		if name, ok := item["name"].(map[string]any); ok {
			track.Language = stringArg(name, "simpleText")
			if track.Language == "" {
				if runs, ok := name["runs"].([]any); ok {
					var parts []string
					for _, run := range runs {
						if entry, ok := run.(map[string]any); ok {
							parts = append(parts, stringArg(entry, "text"))
						}
					}
					track.Language = strings.Join(parts, "")
				}
			}
		}
		if translatable, ok := item["isTranslatable"].(bool); ok {
			track.IsTranslatable = translatable
		}
		tracks = append(tracks, track)
	}
	return tracks
}

// youtubeWatchPage fetches the watch page and returns its HTML.
func youtubeWatchPage(ctx context.Context, rt *Runtime, videoID string) (string, map[string]any) {
	target := batch2BaseURL(rt, "youtube_transcript_tool", nil) + "/watch?v=" + url.QueryEscape(videoID)
	status, body, err := batch2Get(ctx, rt, target, map[string]string{
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
		"Accept-Language": "en-US,en;q=0.9",
	})
	if err != nil {
		return "", map[string]any{"error": "RequestFailed: " + err.Error()}
	}
	if status < 200 || status >= 300 {
		return "", map[string]any{"error": fmt.Sprintf("VideoUnavailable: HTTP %d for %s", status, videoID)}
	}
	return string(body), nil
}

func youtubeListTranscripts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	videoID := strings.TrimSpace(stringArg(input, "video_id"))
	if videoID == "" {
		return map[string]any{"error": "video_id is required"}, nil
	}
	page, problem := youtubeWatchPage(ctx, rt, videoID)
	if problem != nil {
		return problem, nil
	}
	tracks := youtubeCaptionTracks(page)
	if len(tracks) == 0 {
		return map[string]any{"error": fmt.Sprintf("NoTranscriptFound: No transcripts available for %s", videoID)}, nil
	}
	transcripts := make([]map[string]any, 0, len(tracks))
	for _, track := range tracks {
		transcripts = append(transcripts, map[string]any{
			"language":        track.Language,
			"language_code":   track.LanguageCode,
			"is_generated":    track.IsGenerated,
			"is_translatable": track.IsTranslatable,
		})
	}
	return map[string]any{"video_id": videoID, "transcripts": transcripts, "count": len(transcripts)}, nil
}

type youtubeTimedText struct {
	Texts []struct {
		Start string `xml:"start,attr"`
		Dur   string `xml:"dur,attr"`
		Text  string `xml:",chardata"`
	} `xml:"text"`
}

// youtubeTrackURL appends the JSON3 format request to a caption base URL.
func youtubeTrackURL(baseURL string) string {
	if strings.Contains(baseURL, "fmt=") {
		return baseURL
	}
	if strings.Contains(baseURL, "?") {
		return baseURL + "&fmt=json3"
	}
	return baseURL + "?fmt=json3"
}

// youtubeSnippets converts a timedtext payload (json3 or XML) into the
// upstream snippet list.
func youtubeSnippets(body []byte, preserveFormatting bool) []map[string]any {
	clean := func(text string) string {
		if preserveFormatting {
			return text
		}
		return cleanHTMLText(text)
	}
	var json3 struct {
		Events []struct {
			StartMs    float64 `json:"tStartMs"`
			DurationMs float64 `json:"dDurationMs"`
			Segs       []struct {
				UTF8 string `json:"utf8"`
			} `json:"segs"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &json3); err == nil && len(json3.Events) > 0 {
		snippets := make([]map[string]any, 0, len(json3.Events))
		for _, event := range json3.Events {
			var parts []string
			for _, seg := range event.Segs {
				parts = append(parts, seg.UTF8)
			}
			text := clean(strings.TrimSpace(strings.Join(parts, "")))
			if text == "" {
				continue
			}
			snippets = append(snippets, map[string]any{
				"text":     text,
				"start":    event.StartMs / 1000,
				"duration": event.DurationMs / 1000,
			})
		}
		return snippets
	}
	var timed youtubeTimedText
	if err := xml.Unmarshal(body, &timed); err != nil {
		return nil
	}
	snippets := make([]map[string]any, 0, len(timed.Texts))
	for _, item := range timed.Texts {
		text := clean(strings.TrimSpace(item.Text))
		start, _ := batch2Number(item.Start)
		duration, _ := batch2Number(item.Dur)
		snippets = append(snippets, map[string]any{
			"text":     text,
			"start":    start,
			"duration": duration,
		})
	}
	return snippets
}

func youtubeGetTranscript(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	videoID := strings.TrimSpace(stringArg(input, "video_id"))
	if videoID == "" {
		return map[string]any{"error": "video_id is required"}, nil
	}
	language := strings.TrimSpace(stringArg(input, "language"))
	if language == "" {
		language = "en"
	}
	page, problem := youtubeWatchPage(ctx, rt, videoID)
	if problem != nil {
		return problem, nil
	}
	tracks := youtubeCaptionTracks(page)
	if len(tracks) == 0 {
		return map[string]any{"error": fmt.Sprintf("NoTranscriptFound: No transcripts available for %s", videoID)}, nil
	}
	chosen := tracks[0]
	found := false
	for _, track := range tracks {
		if track.LanguageCode == language || strings.HasPrefix(track.LanguageCode, language+"-") {
			chosen = track
			found = true
			break
		}
	}
	if !found {
		for _, track := range tracks {
			if strings.HasPrefix(strings.ToLower(track.Language), strings.ToLower(language)) {
				chosen = track
				found = true
				break
			}
		}
	}
	status, body, err := batch2Get(ctx, rt, youtubeTrackURL(chosen.BaseURL), map[string]string{"User-Agent": "Mozilla/5.0"})
	if err != nil {
		return map[string]any{"error": "YouTubeTranscriptApiException: " + err.Error()}, nil
	}
	if status < 200 || status >= 300 {
		return map[string]any{"error": fmt.Sprintf("YouTubeTranscriptApiException: HTTP %d", status)}, nil
	}
	snippets := youtubeSnippets(body, boolArg(input, "preserve_formatting", false))
	if len(snippets) == 0 {
		return map[string]any{"error": "YouTubeTranscriptApiException: transcript is empty"}, nil
	}
	total := len(snippets)
	if total > 500 {
		snippets = snippets[:500]
	}
	return map[string]any{
		"video_id":      videoID,
		"language":      chosen.Language,
		"language_code": chosen.LanguageCode,
		"is_generated":  chosen.IsGenerated,
		"snippets":      snippets,
		"snippet_count": total,
	}, nil
}
