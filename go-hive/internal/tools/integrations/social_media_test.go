package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type batch7Lane7RecordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Form   url.Values
	Body   string
}

type batch7Lane7Recorder struct {
	mu       sync.Mutex
	requests []batch7Lane7RecordedRequest
}

func (rec *batch7Lane7Recorder) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		form, _ := url.ParseQuery(string(raw))
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch7Lane7RecordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Form:   form,
			Body:   string(raw),
		})
		rec.mu.Unlock()
		next(w, r)
	}
}

func (rec *batch7Lane7Recorder) at(t *testing.T, index int) batch7Lane7RecordedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch7Lane7Recorder) last(t *testing.T) batch7Lane7RecordedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func batch7Lane7WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func batch7Lane7StatusHandler(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane7WriteJSON(w, status, body)
	}
}

// batch7Lane7TestRegistry points one module at a fake upstream and injects
// credentials through the runtime environment lookup.
func batch7Lane7TestRegistry(
	t *testing.T,
	module string,
	env map[string]string,
	handler http.HandlerFunc,
	opts ...Option,
) (*Registry, *batch7Lane7Recorder) {
	t.Helper()
	recorder := &batch7Lane7Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	options := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
	}
	options = append(options, opts...)
	return newBatch7Lane7Registry(options...), recorder
}

func batch7Lane7Run(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

func batch7Lane7WantOK(t *testing.T, result map[string]any) {
	t.Helper()
	if message, failed := result["error"]; failed {
		t.Fatalf("unexpected error: %v", message)
	}
}

func batch7Lane7ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("error = %v (%T), want string", result["error"], result["error"])
	}
	return text
}

func batch7Lane7FieldMap(t *testing.T, object map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := object[name].(map[string]any)
	if !ok {
		t.Fatalf("field %q = %v (%T), want object", name, object[name], object[name])
	}
	return value
}

func batch7Lane7FieldSlice(t *testing.T, object map[string]any, name string) []any {
	t.Helper()
	value, ok := object[name].([]any)
	if !ok {
		t.Fatalf("field %q = %v (%T), want array", name, object[name], object[name])
	}
	return value
}

func batch7Lane7FieldString(t *testing.T, object map[string]any, name string) string {
	t.Helper()
	value, ok := object[name].(string)
	if !ok {
		t.Fatalf("field %q = %v (%T), want string", name, object[name], object[name])
	}
	return value
}

func batch7Lane7FieldInt(t *testing.T, object map[string]any, name string) int {
	t.Helper()
	return batch7Lane7Int(object[name])
}

// ---------------------------------------------------------------- YouTube ---

// Parity-exempt: test_youtube_tool.py covers search/details/channel/playlist/
// comments/duration through class-style tests; the remaining actions
// (list_channel_videos, search_channels, video_categories) have no upstream
// unit test, so this table exercises all eight actions against a fake upstream.
func TestBatch7Lane7YouTubeActions(t *testing.T) {
	env := map[string]string{"YOUTUBE_API_KEY": "test-key"}

	t.Run("youtube_search_videos", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"pageInfo": map[string]any{"totalResults": 2},
				"items": []any{
					map[string]any{
						"id": map[string]any{"videoId": "v1"},
						"snippet": map[string]any{
							"title": "First", "channelTitle": "Chan", "channelId": "c1",
							"publishedAt": "2024-01-01T00:00:00Z", "description": "desc",
							"thumbnails": map[string]any{"medium": map[string]any{"url": "https://img/medium.jpg"}},
						},
					},
					map[string]any{"id": map[string]any{"videoId": "v2"}, "snippet": map[string]any{"title": "Second"}},
				},
			})
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_search_videos", "query": "go"})
		batch7Lane7WantOK(t, result)
		if got := len(batch7Lane7FieldSlice(t, result, "results")); got != 2 {
			t.Fatalf("results = %d, want 2", got)
		}
		if got := batch7Lane7FieldInt(t, result, "total_results"); got != 2 {
			t.Fatalf("total_results = %d, want 2", got)
		}
		first := batch7Lane7Map(batch7Lane7FieldSlice(t, result, "results")[0])
		if got := batch7Lane7FieldString(t, first, "videoId"); got != "v1" {
			t.Fatalf("videoId = %q", got)
		}
		request := recorder.last(t)
		if request.Path != "/search" || request.Query.Get("q") != "go" || request.Query.Get("type") != "video" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
		if request.Query.Get("key") != "test-key" || request.Query.Get("maxResults") != "10" || request.Query.Get("order") != "relevance" {
			t.Fatalf("query = %s", request.Query.Encode())
		}
	})

	t.Run("youtube_get_video_details", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{
				"id": "v1",
				"snippet": map[string]any{
					"title": "Video", "description": "Body", "channelTitle": "Chan", "channelId": "c1",
					"publishedAt": "2024-01-01", "tags": []any{"go", "hive"}, "categoryId": "28",
					"thumbnails": map[string]any{"high": map[string]any{"url": "https://img/high.jpg"}},
				},
				"contentDetails": map[string]any{"duration": "PT1H2M3S"},
				"statistics":     map[string]any{"viewCount": "123", "likeCount": "7", "commentCount": "2"},
			}}})
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_get_video_details", "video_ids": "v1"})
		batch7Lane7WantOK(t, result)
		videos := batch7Lane7FieldSlice(t, result, "videos")
		if len(videos) != 1 {
			t.Fatalf("videos = %d", len(videos))
		}
		video := batch7Lane7Map(videos[0])
		if got := batch7Lane7FieldString(t, video, "duration"); got != "1h2m3s" {
			t.Fatalf("duration = %q", got)
		}
		if got := batch7Lane7FieldString(t, video, "duration_raw"); got != "PT1H2M3S" {
			t.Fatalf("duration_raw = %q", got)
		}
		if got := batch7Lane7FieldInt(t, video, "viewCount"); got != 123 {
			t.Fatalf("viewCount = %d", got)
		}
		if got := len(batch7Lane7FieldSlice(t, video, "tags")); got != 2 {
			t.Fatalf("tags = %d", got)
		}
	})

	t.Run("youtube_get_channel", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{
				"id": "UC1",
				"snippet": map[string]any{
					"title": "Channel", "description": "About", "customUrl": "@chan",
					"publishedAt": "2020-01-01",
					"thumbnails":  map[string]any{"high": map[string]any{"url": "https://img/chan.jpg"}},
				},
				"statistics":     map[string]any{"subscriberCount": "1000", "videoCount": "12", "viewCount": "9000"},
				"contentDetails": map[string]any{"relatedPlaylists": map[string]any{"uploads": "UU1"}},
			}}})
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_get_channel", "handle": "chan"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "uploadsPlaylistId"); got != "UU1" {
			t.Fatalf("uploadsPlaylistId = %q", got)
		}
		if got := batch7Lane7FieldInt(t, result, "subscriberCount"); got != 1000 {
			t.Fatalf("subscriberCount = %d", got)
		}
		if got := recorder.last(t).Query.Get("forHandle"); got != "chan" {
			t.Fatalf("forHandle = %q", got)
		}
	})

	t.Run("youtube_list_channel_videos", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{
				map[string]any{"id": map[string]any{"videoId": "v1"}, "snippet": map[string]any{"title": "One", "publishedAt": "2024-01-01", "description": "d"}},
				map[string]any{"id": map[string]any{"videoId": "v2"}, "snippet": map[string]any{"title": "Two"}},
			}})
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_list_channel_videos", "channel_id": "UC1"})
		batch7Lane7WantOK(t, result)
		if got := len(batch7Lane7FieldSlice(t, result, "videos")); got != 2 {
			t.Fatalf("videos = %d", got)
		}
		if got := recorder.last(t).Query.Get("channelId"); got != "UC1" {
			t.Fatalf("channelId = %q", got)
		}
	})

	t.Run("youtube_get_playlist", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/playlists":
				batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{
					"id":             "PL1",
					"snippet":        map[string]any{"title": "Playlist", "description": "Desc", "channelTitle": "Chan"},
					"contentDetails": map[string]any{"itemCount": 2},
				}}})
			case "/playlistItems":
				batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{
					"snippet": map[string]any{
						"resourceId":             map[string]any{"videoId": "v1"},
						"title":                  "First",
						"position":               0,
						"videoOwnerChannelTitle": "Chan",
						"thumbnails":             map[string]any{"medium": map[string]any{"url": "https://img/p.jpg"}},
					},
				}}})
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_get_playlist", "playlist_id": "PL1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "itemCount"); got != 2 {
			t.Fatalf("itemCount = %d", got)
		}
		items := batch7Lane7FieldSlice(t, result, "items")
		if len(items) != 1 || batch7Lane7FieldString(t, batch7Lane7Map(items[0]), "videoId") != "v1" {
			t.Fatalf("items = %v", items)
		}
		if got := recorder.at(t, 0).Path; got != "/playlists" {
			t.Fatalf("first path = %q", got)
		}
		if got := recorder.at(t, 1).Path; got != "/playlistItems" {
			t.Fatalf("second path = %q", got)
		}
	})

	t.Run("youtube_search_channels", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{
				"id":      map[string]any{"channelId": "UC9"},
				"snippet": map[string]any{"title": "Chan", "description": "About"},
			}}})
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_search_channels", "query": "hive"})
		batch7Lane7WantOK(t, result)
		results := batch7Lane7FieldSlice(t, result, "results")
		if len(results) != 1 || batch7Lane7FieldString(t, batch7Lane7Map(results[0]), "channelId") != "UC9" {
			t.Fatalf("results = %v", results)
		}
		if got := recorder.last(t).Query.Get("type"); got != "channel" {
			t.Fatalf("type = %q", got)
		}
	})

	t.Run("youtube_get_video_comments", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{
				"snippet": map[string]any{
					"totalReplyCount": 3,
					"topLevelComment": map[string]any{"snippet": map[string]any{
						"authorDisplayName": "Alice", "textDisplay": "Nice", "likeCount": 4,
						"publishedAt": "2024-01-01",
					}},
				},
			}}})
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_get_video_comments", "video_id": "v1"})
		batch7Lane7WantOK(t, result)
		comments := batch7Lane7FieldSlice(t, result, "comments")
		if len(comments) != 1 {
			t.Fatalf("comments = %d", len(comments))
		}
		comment := batch7Lane7Map(comments[0])
		if got := batch7Lane7FieldInt(t, comment, "replyCount"); got != 3 {
			t.Fatalf("replyCount = %d", got)
		}
		if got := recorder.last(t).Query.Get("textFormat"); got != "plainText" {
			t.Fatalf("textFormat = %q", got)
		}
	})

	t.Run("youtube_get_video_categories", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"items": []any{
				map[string]any{"id": "10", "snippet": map[string]any{"title": "Music"}},
				map[string]any{"id": "20", "snippet": map[string]any{"title": "Gaming"}},
			}})
		})
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_get_video_categories"})
		batch7Lane7WantOK(t, result)
		if got := len(batch7Lane7FieldSlice(t, result, "categories")); got != 2 {
			t.Fatalf("categories = %d", got)
		}
		if got := recorder.last(t).Query.Get("regionCode"); got != "US" {
			t.Fatalf("regionCode = %q", got)
		}
	})
}

// Parity: tools/tests/tools/test_youtube_tool.py::TestParseDuration
func TestBatch7Lane7YouTubeParseDuration(t *testing.T) {
	cases := map[string]string{
		"PT1H2M3S": "1h2m3s",
		"PT1H":     "1h",
		"PT5M":     "5m",
		"PT45S":    "45s",
		"PT0S":     "0s",
		"":         "",
		"P1D":      "P1D",
	}
	for input, want := range cases {
		if got := batch7Lane7ParseDuration(input); got != want {
			t.Fatalf("%q -> %q, want %q", input, got, want)
		}
	}
}

// ----------------------------------------------------------------- Reddit ---

func batch7Lane7RedditPostChild(id, title, author, subreddit string) map[string]any {
	return map[string]any{
		"kind": "t3",
		"data": map[string]any{
			"id": id, "title": title, "author": author, "subreddit": subreddit,
			"score": 42, "num_comments": 5, "url": "https://example/" + id,
			"permalink": "/r/" + subreddit + "/comments/" + id, "selftext": "self text",
			"created_utc": 1700000000, "is_self": true,
		},
	}
}

func batch7Lane7RedditListing(children ...any) map[string]any {
	return map[string]any{"data": map[string]any{"children": children}}
}

// Parity-exempt: test_reddit_tool.py covers search/posts/comments/user through
// class-style tests; subreddit_info, post_detail and user_posts have no
// upstream unit test, so this table exercises all seven actions.
func TestBatch7Lane7RedditActions(t *testing.T) {
	const token = "reddit-token"
	opts := []Option{WithCredential(batch7Lane7RedditModule, token)}
	checkAuth := func(t *testing.T, header http.Header) {
		t.Helper()
		if got := header.Get("Authorization"); got != "bearer "+token {
			t.Fatalf("Authorization = %q", got)
		}
	}

	t.Run("reddit_search", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, batch7Lane7RedditListing(
				batch7Lane7RedditPostChild("p1", "Post", "alice", "golang"),
				map[string]any{"kind": "t1", "data": map[string]any{"id": "c1"}},
			))
		}, opts...)
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{
			"action": "reddit_search", "query": "go", "subreddit": "golang",
		})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		request := recorder.last(t)
		checkAuth(t, request.Header)
		if request.Path != "/r/golang/search" || request.Query.Get("restrict_sr") != "true" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
		if request.Query.Get("q") != "go" || request.Query.Get("limit") != "25" {
			t.Fatalf("query = %s", request.Query.Encode())
		}
	})

	t.Run("reddit_get_posts", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, batch7Lane7RedditListing(
				batch7Lane7RedditPostChild("p1", "Post", "alice", "golang"),
			))
		}, opts...)
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{
			"action": "reddit_get_posts", "subreddit": "golang",
		})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		request := recorder.last(t)
		checkAuth(t, request.Header)
		if request.Path != "/r/golang/hot" || request.Query.Get("t") != "day" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
	})

	t.Run("reddit_get_comments", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, []any{
				batch7Lane7RedditListing(batch7Lane7RedditPostChild("p1", "Post", "alice", "golang")),
				batch7Lane7RedditListing(map[string]any{
					"kind": "t1",
					"data": map[string]any{
						"id": "c1", "author": "bob", "body": "hello", "score": 3,
						"created_utc": 1700000001,
					},
				}),
			})
		}, opts...)
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{
			"action": "reddit_get_comments", "post_id": "p1", "subreddit": "golang",
		})
		batch7Lane7WantOK(t, result)
		post := batch7Lane7FieldMap(t, result, "post")
		if got := batch7Lane7FieldString(t, post, "title"); got != "Post" {
			t.Fatalf("post title = %q", got)
		}
		if got := batch7Lane7FieldInt(t, result, "comment_count"); got != 1 {
			t.Fatalf("comment_count = %d", got)
		}
		if got := recorder.last(t).Path; got != "/r/golang/comments/p1" {
			t.Fatalf("path = %q", got)
		}
	})

	t.Run("reddit_get_user", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"name": "alice", "link_karma": 10, "comment_karma": 20,
				"total_karma": 30, "created_utc": 1600000000, "is_gold": true,
			}})
		}, opts...)
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_user", "username": "alice"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "total_karma"); got != 30 {
			t.Fatalf("total_karma = %d", got)
		}
		if !batch7Lane7Bool(result["is_gold"]) {
			t.Fatal("is_gold = false")
		}
	})

	t.Run("reddit_get_subreddit_info", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"display_name": "golang", "title": "Go", "public_description": "The Go language",
				"subscribers": 100, "accounts_active": 5, "created_utc": 1200000000,
				"over18": false, "subreddit_type": "public", "submission_type": "any",
			}})
		}, opts...)
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_subreddit_info", "subreddit": "golang"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "name"); got != "golang" {
			t.Fatalf("name = %q", got)
		}
		if got := batch7Lane7FieldInt(t, result, "subscribers"); got != 100 {
			t.Fatalf("subscribers = %d", got)
		}
	})

	t.Run("reddit_get_post_detail", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, batch7Lane7RedditListing(
				batch7Lane7RedditPostChild("p1", "Full", "alice", "golang"),
			))
		}, opts...)
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_post_detail", "post_id": "p1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "title"); got != "Full" {
			t.Fatalf("title = %q", got)
		}
		if got := recorder.last(t).Path; got != "/by_id/t3_p1" {
			t.Fatalf("path = %q", got)
		}
	})

	t.Run("reddit_get_user_posts", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, batch7Lane7RedditListing(
				batch7Lane7RedditPostChild("p1", "Mine", "alice", "golang"),
			))
		}, opts...)
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_user_posts", "username": "alice"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		request := recorder.last(t)
		if request.Path != "/user/alice/submitted" || request.Query.Get("sort") != "new" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
	})
}

// Parity: tools/tests/tools/test_reddit_tool.py::TestRedditGetPosts
func TestBatch7Lane7RedditTokenFlow(t *testing.T) {
	recorder := &batch7Lane7Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/access_token":
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("client-id:client-secret"))
			if got := r.Header.Get("Authorization"); got != want {
				t.Errorf("token Authorization = %q", got)
			}
			if got := r.FormValue("grant_type"); got != "client_credentials" {
				t.Errorf("grant_type = %q", got)
			}
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"access_token": "acquired-token"})
		case "/r/golang/hot":
			if got := r.Header.Get("Authorization"); got != "bearer acquired-token" {
				t.Errorf("api Authorization = %q", got)
			}
			batch7Lane7WriteJSON(w, http.StatusOK, batch7Lane7RedditListing(
				batch7Lane7RedditPostChild("p1", "Post", "alice", "golang"),
			))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	env := map[string]string{
		"REDDIT_CLIENT_ID":     "client-id",
		"REDDIT_CLIENT_SECRET": "client-secret",
		"REDDIT_TOKEN_URL":     server.URL + "/api/v1/access_token",
	}
	registry := newBatch7Lane7Registry(
		WithBaseURL(batch7Lane7RedditModule, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
	)
	result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_posts", "subreddit": "golang"})
	batch7Lane7WantOK(t, result)
	if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
		t.Fatalf("count = %d", got)
	}
	if got := recorder.at(t, 0).Path; got != "/api/v1/access_token" {
		t.Fatalf("first path = %q", got)
	}
	if got := recorder.at(t, 1).Path; got != "/r/golang/hot" {
		t.Fatalf("second path = %q", got)
	}
}

// Parity-exempt: upstream unit tests mock the whole client; this Go-side test
// pins the OAuth client-credentials failure path to the upstream error string.
func TestBatch7Lane7RedditTokenFailure(t *testing.T) {
	recorder := &batch7Lane7Recorder{}
	server := httptest.NewServer(recorder.wrap(batch7Lane7StatusHandler(
		http.StatusUnauthorized, map[string]any{"error": "invalid_client"},
	)))
	t.Cleanup(server.Close)
	env := map[string]string{
		"REDDIT_CLIENT_ID":     "client-id",
		"REDDIT_CLIENT_SECRET": "client-secret",
		"REDDIT_TOKEN_URL":     server.URL + "/api/v1/access_token",
	}
	registry := newBatch7Lane7Registry(
		WithBaseURL(batch7Lane7RedditModule, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
	)
	result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_posts", "subreddit": "golang"})
	if got := batch7Lane7ErrorText(t, result); got != "Failed to acquire Reddit access token" {
		t.Fatalf("error = %q", got)
	}
}

// ---------------------------------------------------------------- Twitter ---

// Parity-exempt: test_twitter_tool.py covers search/user/user_tweets/tweet;
// followers, replies and list_tweets have no upstream unit test, so this
// table exercises all seven actions.
func TestBatch7Lane7TwitterActions(t *testing.T) {
	env := map[string]string{"X_BEARER_TOKEN": "bearer-token"}

	t.Run("twitter_search_tweets", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{
					"id": "t1", "text": "hello", "author_id": "u1", "created_at": "2024-01-01",
					"lang": "en", "public_metrics": map[string]any{"retweet_count": 2, "reply_count": 1, "like_count": 9, "impression_count": 100},
				}},
				"includes": map[string]any{"users": []any{map[string]any{"id": "u1", "name": "Alice", "username": "alice"}}},
				"meta":     map[string]any{"result_count": 1},
			})
		})
		result := batch7Lane7Run(t, registry, "twitter", map[string]any{"action": "twitter_search_tweets", "query": "go"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		tweet := batch7Lane7Map(batch7Lane7FieldSlice(t, result, "tweets")[0])
		if got := batch7Lane7FieldString(t, tweet, "author_username"); got != "alice" {
			t.Fatalf("author_username = %q", got)
		}
		request := recorder.last(t)
		if request.Path != "/tweets/search/recent" || request.Query.Get("max_results") != "10" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer bearer-token" {
			t.Fatalf("Authorization = %q", got)
		}
	})

	t.Run("twitter_get_user", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
				"id": "u1", "name": "Alice", "username": "alice", "description": "hi",
				"created_at": "2020-01-01", "profile_image_url": "https://img", "verified": true,
				"public_metrics": map[string]any{"followers_count": 10, "following_count": 2, "tweet_count": 5},
			}})
		})
		result := batch7Lane7Run(t, registry, "twitter", map[string]any{"action": "twitter_get_user", "username": "alice"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "followers_count"); got != 10 {
			t.Fatalf("followers_count = %d", got)
		}
	})

	t.Run("twitter_get_user_tweets", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "t1", "text": "hello", "public_metrics": map[string]any{"like_count": 3},
			}}})
		})
		result := batch7Lane7Run(t, registry, "twitter", map[string]any{
			"action": "twitter_get_user_tweets", "user_id": "u1", "max_results": 1,
		})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		request := recorder.last(t)
		if request.Path != "/users/u1/tweets" || request.Query.Get("max_results") != "5" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
		if got := request.Query.Get("exclude"); got != "replies,retweets" {
			t.Fatalf("exclude = %q", got)
		}
	})

	t.Run("twitter_get_tweet", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"data":     map[string]any{"id": "t1", "text": "hello", "author_id": "u1"},
				"includes": map[string]any{"users": []any{map[string]any{"id": "u1", "name": "Alice", "username": "alice"}}},
			})
		})
		result := batch7Lane7Run(t, registry, "twitter", map[string]any{"action": "twitter_get_tweet", "tweet_id": "t1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "author_name"); got != "Alice" {
			t.Fatalf("author_name = %q", got)
		}
	})

	t.Run("twitter_get_user_followers", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]any{
				"id": "u2", "name": "Bob", "username": "bob", "description": "desc", "verified": false,
				"public_metrics": map[string]any{"followers_count": 4, "following_count": 1},
			}}})
		})
		result := batch7Lane7Run(t, registry, "twitter", map[string]any{"action": "twitter_get_user_followers", "user_id": "u1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		if got := recorder.last(t).Path; got != "/users/u1/followers" {
			t.Fatalf("path = %q", got)
		}
	})

	t.Run("twitter_get_tweet_replies", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"data":     []any{map[string]any{"id": "t2", "text": "reply", "author_id": "u1"}},
				"includes": map[string]any{"users": []any{map[string]any{"id": "u1", "name": "Alice", "username": "alice"}}},
			})
		})
		result := batch7Lane7Run(t, registry, "twitter", map[string]any{"action": "twitter_get_tweet_replies", "tweet_id": "t1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		if got := recorder.last(t).Query.Get("query"); got != "conversation_id:t1 is:reply" {
			t.Fatalf("query = %q", got)
		}
	})

	t.Run("twitter_get_list_tweets", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"data":     []any{map[string]any{"id": "t1", "text": "list", "author_id": "u1"}},
				"includes": map[string]any{"users": []any{map[string]any{"id": "u1", "name": "Alice", "username": "alice"}}},
			})
		})
		result := batch7Lane7Run(t, registry, "twitter", map[string]any{"action": "twitter_get_list_tweets", "list_id": "list1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		if got := recorder.last(t).Path; got != "/lists/list1/tweets" {
			t.Fatalf("path = %q", got)
		}
	})
}

// ----------------------------------------------------------------- Twilio ---

// Parity-exempt: test_twilio_tool.py covers send_sms/send_whatsapp/
// list_messages/get_message; phone_numbers, calls and delete have no upstream
// unit test, so this table exercises all seven actions.
func TestBatch7Lane7TwilioActions(t *testing.T) {
	env := map[string]string{
		"TWILIO_ACCOUNT_SID": "AC123",
		"TWILIO_AUTH_TOKEN":  "secret",
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("AC123:secret"))
	checkAuth := func(t *testing.T, header http.Header) {
		t.Helper()
		if got := header.Get("Authorization"); got != wantAuth {
			t.Fatalf("Authorization = %q", got)
		}
	}
	message := map[string]any{
		"sid": "SM1", "to": "+15550001", "from": "+15550002", "body": "hi",
		"status": "queued", "direction": "outbound-api", "date_sent": nil,
		"price": nil, "error_code": nil, "error_message": nil,
	}

	t.Run("twilio_send_sms", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusCreated, message)
		})
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{
			"action": "twilio_send_sms", "to": "+15550001", "from_number": "+15550002", "body": "hi",
		})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "sid"); got != "SM1" {
			t.Fatalf("sid = %q", got)
		}
		request := recorder.last(t)
		checkAuth(t, request.Header)
		if request.Path != "/2010-04-01/Accounts/AC123/Messages.json" || request.Method != http.MethodPost {
			t.Fatalf("request = %s %s", request.Method, request.Path)
		}
		if got := request.Form.Get("To"); got != "+15550001" {
			t.Fatalf("To = %q", got)
		}
		if got := request.Form.Get("Body"); got != "hi" {
			t.Fatalf("Body = %q", got)
		}
	})

	t.Run("twilio_send_whatsapp", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusCreated, message)
		})
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{
			"action": "twilio_send_whatsapp", "to": "+15550001", "from_number": "+15550002", "body": "hi",
		})
		batch7Lane7WantOK(t, result)
		form := recorder.last(t).Form
		if got := form.Get("To"); got != "whatsapp:+15550001" {
			t.Fatalf("To = %q", got)
		}
		if got := form.Get("From"); got != "whatsapp:+15550002" {
			t.Fatalf("From = %q", got)
		}
	})

	t.Run("twilio_list_messages", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"messages": []any{message}})
		})
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_list_messages"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		request := recorder.last(t)
		if request.Method != http.MethodGet || request.Query.Get("PageSize") != "20" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
	})

	t.Run("twilio_get_message", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, message)
		})
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_get_message", "message_sid": "SM1"})
		batch7Lane7WantOK(t, result)
		if got := recorder.last(t).Path; got != "/2010-04-01/Accounts/AC123/Messages/SM1.json" {
			t.Fatalf("path = %q", got)
		}
	})

	t.Run("twilio_list_phone_numbers", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"incoming_phone_numbers": []any{map[string]any{
				"sid": "PN1", "phone_number": "+15550003", "friendly_name": "Main",
				"capabilities": map[string]any{"sms": true, "voice": true, "mms": false},
				"date_created": "2020-01-01",
			}}})
		})
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_list_phone_numbers"})
		batch7Lane7WantOK(t, result)
		numbers := batch7Lane7FieldSlice(t, result, "phone_numbers")
		if len(numbers) != 1 {
			t.Fatalf("phone_numbers = %d", len(numbers))
		}
		number := batch7Lane7Map(numbers[0])
		if !batch7Lane7Bool(number["sms_enabled"]) || batch7Lane7Bool(number["mms_enabled"]) {
			t.Fatalf("capabilities = %v", number)
		}
		if got := recorder.last(t).Query.Get("PageSize"); got != "100" {
			t.Fatalf("PageSize = %q", got)
		}
	})

	t.Run("twilio_list_calls", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"calls": []any{map[string]any{
				"sid": "CA1", "to": "+15550001", "from": "+15550002", "status": "completed",
				"direction": "outbound-api", "duration": "5", "price": "-0.01",
				"start_time": "2024-01-01", "end_time": "2024-01-01",
			}}})
		})
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_list_calls", "status": "completed"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "count"); got != 1 {
			t.Fatalf("count = %d", got)
		}
		if got := recorder.last(t).Query.Get("Status"); got != "completed" {
			t.Fatalf("Status = %q", got)
		}
	})

	t.Run("twilio_delete_message", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, env, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_delete_message", "message_sid": "SM1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "status"); got != "deleted" {
			t.Fatalf("status = %q", got)
		}
		request := recorder.last(t)
		if request.Method != http.MethodDelete || request.Path != "/2010-04-01/Accounts/AC123/Messages/SM1.json" {
			t.Fatalf("request = %s %s", request.Method, request.Path)
		}
	})
}

// --------------------------------------------------------------- Pushover ---

// Parity-exempt: test_pushover_tool.py covers send/validate/sounds/receipt;
// cancel_receipt, send_glance and get_limits have no upstream unit test, so
// this table exercises all seven actions.
func TestBatch7Lane7PushoverActions(t *testing.T) {
	env := map[string]string{"PUSHOVER_API_TOKEN": "app-token"}

	t.Run("pushover_send", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"status": 1, "request": "req1"})
		})
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{
			"action": "pushover_send", "user_key": "user1", "message": "hello", "title": "Hi",
		})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "status"); got != "sent" {
			t.Fatalf("status = %q", got)
		}
		form := recorder.last(t).Form
		if form.Get("token") != "app-token" || form.Get("user") != "user1" || form.Get("message") != "hello" {
			t.Fatalf("form = %s", form.Encode())
		}
		if form.Get("title") != "Hi" {
			t.Fatalf("title = %q", form.Get("title"))
		}
	})

	t.Run("pushover_validate_user", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"status": 1, "devices": []any{"phone", "tablet"}, "group": 1,
			})
		})
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_validate_user", "user_key": "user1"})
		batch7Lane7WantOK(t, result)
		if !batch7Lane7Bool(result["is_valid"]) || !batch7Lane7Bool(result["is_group"]) {
			t.Fatalf("result = %v", result)
		}
		if got := len(batch7Lane7FieldSlice(t, result, "devices")); got != 2 {
			t.Fatalf("devices = %d", got)
		}
	})

	t.Run("pushover_list_sounds", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"status": 1, "sounds": map[string]any{"pushover": "Pushover (default)", "bike": "Bike"},
			})
		})
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_list_sounds"})
		batch7Lane7WantOK(t, result)
		if got := len(batch7Lane7FieldMap(t, result, "sounds")); got != 2 {
			t.Fatalf("sounds = %d", got)
		}
		request := recorder.last(t)
		if request.Method != http.MethodGet || request.Query.Get("token") != "app-token" {
			t.Fatalf("request = %s %s?%s", request.Method, request.Path, request.Query.Encode())
		}
	})

	t.Run("pushover_check_receipt", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{
				"status": 1, "acknowledged": 1, "acknowledged_by": "user1",
				"acknowledged_at": 1700000000, "last_delivered_at": 1699999999,
				"expired": 0, "called_back": 1,
			})
		})
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_check_receipt", "receipt": "r1"})
		batch7Lane7WantOK(t, result)
		if !batch7Lane7Bool(result["acknowledged"]) || !batch7Lane7Bool(result["called_back"]) {
			t.Fatalf("result = %v", result)
		}
		if got := recorder.last(t).Path; got != "/receipts/r1.json" {
			t.Fatalf("path = %q", got)
		}
	})

	t.Run("pushover_cancel_receipt", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"status": 1})
		})
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_cancel_receipt", "receipt": "r1"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldString(t, result, "status"); got != "cancelled" {
			t.Fatalf("status = %q", got)
		}
		if got := recorder.last(t).Path; got != "/receipts/r1/cancel.json" {
			t.Fatalf("path = %q", got)
		}
	})

	t.Run("pushover_send_glance", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"status": 1, "request": "req2"})
		})
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{
			"action": "pushover_send_glance", "user_key": "user1", "title": "Build",
			"count": 1200, "percent": 150,
		})
		batch7Lane7WantOK(t, result)
		form := recorder.last(t).Form
		if got := form.Get("count"); got != "999" {
			t.Fatalf("count = %q", got)
		}
		if got := form.Get("percent"); got != "100" {
			t.Fatalf("percent = %q", got)
		}
	})

	t.Run("pushover_get_limits", func(t *testing.T) {
		registry, recorder := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, env, func(w http.ResponseWriter, _ *http.Request) {
			batch7Lane7WriteJSON(w, http.StatusOK, map[string]any{"status": 1, "limit": 10000, "remaining": 9000, "reset": 1700000000})
		})
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_get_limits"})
		batch7Lane7WantOK(t, result)
		if got := batch7Lane7FieldInt(t, result, "remaining"); got != 9000 {
			t.Fatalf("remaining = %d", got)
		}
		if got := recorder.last(t).Path; got != "/apps/limits.json" {
			t.Fatalf("path = %q", got)
		}
	})
}

// ------------------------------------------------------------ error paths ---

// Parity-exempt: error-status mapping is exercised per action against a fake
// upstream; the upstream classes only pin a subset of the 401/404/429 strings.
func TestBatch7Lane7UpstreamErrors(t *testing.T) {
	t.Run("youtube_quota", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, map[string]string{"YOUTUBE_API_KEY": "key"},
			batch7Lane7StatusHandler(http.StatusForbidden, map[string]any{
				"error": map[string]any{"errors": []any{map[string]any{"reason": "quotaExceeded"}}},
			}))
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_search_videos", "query": "go"})
		if got := batch7Lane7ErrorText(t, result); !strings.Contains(got, "quota exceeded") {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("youtube_401", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7YouTubeModule, map[string]string{"YOUTUBE_API_KEY": "key"},
			batch7Lane7StatusHandler(http.StatusUnauthorized, map[string]any{"error": "bad key"}))
		result := batch7Lane7Run(t, registry, "youtube", map[string]any{"action": "youtube_search_videos", "query": "go"})
		if got := batch7Lane7ErrorText(t, result); !strings.Contains(got, "YouTube API error 401") {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("reddit_401", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil,
			batch7Lane7StatusHandler(http.StatusUnauthorized, map[string]any{"message": "unauthorized"}),
			WithCredential(batch7Lane7RedditModule, "token"))
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_posts", "subreddit": "golang"})
		if got := batch7Lane7ErrorText(t, result); got != "Unauthorized. Reddit token may be expired." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("reddit_403", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil,
			batch7Lane7StatusHandler(http.StatusForbidden, map[string]any{"message": "forbidden"}),
			WithCredential(batch7Lane7RedditModule, "token"))
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_posts", "subreddit": "golang"})
		if got := batch7Lane7ErrorText(t, result); got != "Forbidden. Check Reddit app permissions." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("reddit_429", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7RedditModule, nil,
			batch7Lane7StatusHandler(http.StatusTooManyRequests, map[string]any{"message": "slow down"}),
			WithCredential(batch7Lane7RedditModule, "token"))
		result := batch7Lane7Run(t, registry, "reddit", map[string]any{"action": "reddit_get_posts", "subreddit": "golang"})
		if got := batch7Lane7ErrorText(t, result); !strings.Contains(got, "Reddit API error 429") {
			t.Fatalf("error = %q", got)
		}
	})

	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		status := status
		t.Run(fmt.Sprintf("twitter_%d", status), func(t *testing.T) {
			registry, _ := batch7Lane7TestRegistry(t, batch7Lane7TwitterModule, map[string]string{"X_BEARER_TOKEN": "token"},
				batch7Lane7StatusHandler(status, map[string]any{"error": "provider"}))
			result := batch7Lane7Run(t, registry, "twitter", map[string]any{"action": "twitter_get_user", "username": "alice"})
			if got := batch7Lane7ErrorText(t, result); !strings.Contains(got, fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("error = %q", got)
			}
		})
	}

	t.Run("twilio_401", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, map[string]string{
			"TWILIO_ACCOUNT_SID": "AC1", "TWILIO_AUTH_TOKEN": "token",
		}, batch7Lane7StatusHandler(http.StatusUnauthorized, map[string]any{"message": "unauthorized"}))
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{
			"action": "twilio_send_sms", "to": "+1", "from_number": "+2", "body": "hi",
		})
		if got := batch7Lane7ErrorText(t, result); got != "Unauthorized. Check your Twilio credentials." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("twilio_404", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, map[string]string{
			"TWILIO_ACCOUNT_SID": "AC1", "TWILIO_AUTH_TOKEN": "token",
		}, batch7Lane7StatusHandler(http.StatusNotFound, map[string]any{"message": "missing"}))
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_get_message", "message_sid": "SM1"})
		if got := batch7Lane7ErrorText(t, result); got != "Resource not found." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("twilio_429", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, map[string]string{
			"TWILIO_ACCOUNT_SID": "AC1", "TWILIO_AUTH_TOKEN": "token",
		}, batch7Lane7StatusHandler(http.StatusTooManyRequests, map[string]any{"message": "slow"}))
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_list_messages"})
		if got := batch7Lane7ErrorText(t, result); got != "Rate limited. Try again shortly." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("twilio_delete_404", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7TwilioModule, map[string]string{
			"TWILIO_ACCOUNT_SID": "AC1", "TWILIO_AUTH_TOKEN": "token",
		}, batch7Lane7StatusHandler(http.StatusNotFound, map[string]any{"message": "missing"}))
		result := batch7Lane7Run(t, registry, "twilio", map[string]any{"action": "twilio_delete_message", "message_sid": "SM1"})
		if got := batch7Lane7ErrorText(t, result); got != "Message not found." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("pushover_401", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, map[string]string{"PUSHOVER_API_TOKEN": "token"},
			batch7Lane7StatusHandler(http.StatusUnauthorized, map[string]any{"status": 0, "errors": []any{"invalid token"}}))
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_send", "user_key": "u", "message": "hi"})
		if got := batch7Lane7ErrorText(t, result); got != "Pushover error: invalid token" {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("pushover_404", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, map[string]string{"PUSHOVER_API_TOKEN": "token"},
			batch7Lane7StatusHandler(http.StatusNotFound, map[string]any{"status": 0, "errors": []any{"receipt not found"}}))
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_check_receipt", "receipt": "r1"})
		if got := batch7Lane7ErrorText(t, result); !strings.Contains(got, "Receipt check failed") {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("pushover_429", func(t *testing.T) {
		registry, _ := batch7Lane7TestRegistry(t, batch7Lane7PushoverModule, map[string]string{"PUSHOVER_API_TOKEN": "token"},
			batch7Lane7StatusHandler(http.StatusTooManyRequests, map[string]any{"status": 0, "errors": []any{"rate limited"}}))
		result := batch7Lane7Run(t, registry, "pushover", map[string]any{"action": "pushover_list_sounds"})
		if got := batch7Lane7ErrorText(t, result); !strings.Contains(got, "Failed to list sounds") {
			t.Fatalf("error = %q", got)
		}
	})
}

// Parity-exempt: missing-credential short-circuits are Go-side contracts; the
// upstream credential store returns equivalent help dicts.
func TestBatch7Lane7CredentialErrors(t *testing.T) {
	registry := newBatch7Lane7Registry(WithGetenv(func(string) string { return "" }))
	cases := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"youtube", map[string]any{"action": "youtube_search_videos", "query": "go"}, "YOUTUBE_API_KEY not set"},
		{"reddit", map[string]any{"action": "reddit_search", "query": "go"}, "REDDIT_CLIENT_ID and REDDIT_CLIENT_SECRET not set"},
		{"twitter", map[string]any{"action": "twitter_get_user", "username": "alice"}, "X_BEARER_TOKEN is required"},
		{"twilio", map[string]any{"action": "twilio_send_sms", "to": "+1", "from_number": "+2", "body": "hi"}, "TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN not set"},
		{"pushover", map[string]any{"action": "pushover_send", "user_key": "u", "message": "hi"}, "PUSHOVER_API_TOKEN not set"},
	}
	for _, tc := range cases {
		result := batch7Lane7Run(t, registry, tc.tool, tc.input)
		if got := batch7Lane7ErrorText(t, result); got != tc.want {
			t.Fatalf("%s error = %q, want %q", tc.tool, got, tc.want)
		}
	}
}

// ------------------------------------------------------------- registry ----

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not the registry surface.
func TestBatch7Lane7RegistrySurface(t *testing.T) {
	registry := newBatch7Lane7Registry()
	if got := len(registry.Names()); got != 5 {
		t.Fatalf("tools = %d, want 5 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 36 {
		t.Fatalf("actions = %d, want 36", got)
	}
	if got := registry.ImplementedActions(); got != 36 {
		t.Fatalf("implemented = %d, want 36", got)
	}
	expected := map[string]int{
		"youtube": 8, "reddit": 7, "twitter": 7, "twilio": 7, "pushover": 7,
	}
	for name, want := range expected {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if got := len(tool.Actions); got != want {
			t.Fatalf("%s actions = %d, want %d", name, got, want)
		}
		if tool.ID == "" || tool.Summary == "" || tool.DisplayName == "" {
			t.Fatalf("%s identity incomplete: %+v", name, tool)
		}
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", name, action.Name)
			}
			if registry.Impls(name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}

	shared := tools.NewToolRegistry()
	if got := InstallBatch7Lane7(shared); got != 5 {
		t.Fatalf("installed = %d, want 5", got)
	}
	for name, count := range expected {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
		tool, ok := shared.GetTool(name)
		if !ok {
			t.Fatalf("%s missing from shared registry", name)
		}
		properties, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties = %v", name, tool.Parameters["properties"])
		}
		action, ok := properties["action"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing action selector", name)
		}
		enum, _ := action["enum"].([]string)
		if len(enum) != count {
			t.Fatalf("%s action enum = %v", name, action["enum"])
		}
		if tool.Description == "" {
			t.Fatalf("%s empty description", name)
		}
	}

	if _, err := registry.Execute(context.Background(), "youtube", map[string]any{"action": "nope"}); err == nil {
		t.Fatal("expected unknown action error")
	}
}
