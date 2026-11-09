package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"readtrace/internal/weread"
)

func TestShelfArchivePaginationIncludesBooksAfterFifty(t *testing.T) {
	ids := []string{"main", "archive-01"}
	for i := 1; i <= 52; i++ {
		ids = append(ids, fmt.Sprintf("archive-%02d", i))
	}
	mux := http.NewServeMux()
	var changed atomic.Bool
	mux.HandleFunc("/web/shelf/sync", func(w http.ResponseWriter, r *http.Request) {
		pageIDs := ids
		if changed.Load() {
			pageIDs = append([]string{"new-archive-book"}, ids...)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bookCount": 53, "books": []map[string]string{{"bookId": "main", "title": "main book"}},
			"archive": []map[string]any{{"bookIds": pageIDs}},
		})
	})
	mux.HandleFunc("/web/book/info", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("bookId")
		if id == "archive-51" {
			http.Error(w, "temporary detail failure", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"bookId": id, "title": id})
	})
	mux.HandleFunc("/web/book/chapterInfos", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)
	type page struct {
		Books     []weread.Book     `json:"books"`
		AccountID int64             `json:"account_id"`
		Total     int               `json:"archive_total"`
		Processed int               `json:"archive_processed"`
		Next      int               `json:"next_archive_offset"`
		Failures  map[string]string `json:"archive_failures"`
		Revision  string            `json:"archive_revision"`
	}
	revision := ""
	get := func(offset string) page {
		t.Helper()
		w := httptest.NewRecorder()
		s.handlePoolShelf(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/pool/shelf?account_id=%d&archive_offset=%s&archive_revision=%s", id, offset, revision), nil))
		if w.Code != http.StatusOK {
			t.Fatalf("offset %s: %d %s", offset, w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := get("0")
	revision = first.Revision
	if len(first.Books) != 51 || first.Total != 52 || first.Processed != 50 || first.Next != 50 || first.AccountID != id {
		t.Fatalf("first page: %+v", first)
	}
	second := get("50")
	if len(second.Books) != 1 || second.Books[0].ID != "archive-52" || second.Next != -1 || second.Processed != 52 || second.Failures["archive-51"] == "" {
		t.Fatalf("second page: %+v", second)
	}
	for _, offset := range []string{"-1", "invalid", "53"} {
		w := httptest.NewRecorder()
		s.handlePoolShelf(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/pool/shelf?account_id=%d&archive_offset=%s", id, offset), nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid offset %q: %d", offset, w.Code)
		}
	}
	changed.Store(true)
	w := httptest.NewRecorder()
	s.handlePoolShelf(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/pool/shelf?account_id=%d&archive_offset=50&archive_revision=%s", id, revision), nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("changed shelf silently paginated: %d %s", w.Code, w.Body.String())
	}
}
