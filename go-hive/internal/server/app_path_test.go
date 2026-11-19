// Parity-exempt: Go 特有的 ServeMux 路径清洗行为（307 重定向出 API 面），
// 上游 Python 无对应实现，无法映射到具体 Python 测试。
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRejectDotSegmentsAPIMiddleware(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})
	handler := (&App{}).rejectDotSegmentsAPI(next)

	cases := []struct {
		name   string
		target string
		want   int
	}{
		{"plain dot segments", "/api/sessions/s1/attachment/../../../../etc/passwd", http.StatusBadRequest},
		{"encoded dot segments", "/api/sessions/s1/attachment/%2e%2e/%2e%2e/etc/passwd", http.StatusBadRequest},
		{"trailing dot segment", "/api/sessions/..", http.StatusBadRequest},
		{"ordinary api path", "/api/health", http.StatusNoContent},
		{"non api path untouched", "/assets/../index.html", http.StatusNoContent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodGet, "http://example.test"+tc.target, nil)
			req.URL.Path = tc.target
			if tc.name == "encoded dot segments" {
				req.URL.RawPath = tc.target
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if wantReach := tc.want == http.StatusNoContent; reached != wantReach {
				t.Fatalf("next reached = %v, want %v", reached, wantReach)
			}
		})
	}
}
