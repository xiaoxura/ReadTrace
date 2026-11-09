package weread

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchBooksDistinguishesBusinessErrorsFromNoMatches(t *testing.T) {
	for _, tc := range []struct {
		name, body        string
		wantErr, wantAuth bool
		wantBooks         int
	}{
		{"expired", `{"errCode":-2012,"errMsg":"session expired"}`, true, true, 0},
		{"transient", `{"errCode":-2003,"errMsg":"try later"}`, true, false, 0},
		{"no matches", `{"books":[]}`, false, false, 0},
		{"success", `{"errCode":0,"books":[{"bookInfo":{"bookId":"123456","title":"match"}}]}`, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := NewClient()
			client.Base = srv.URL
			books, err := client.SearchBooks(context.Background(), nil, "query")
			if (err != nil) != tc.wantErr || errors.Is(err, ErrUnauthorized) != tc.wantAuth || len(books) != tc.wantBooks {
				t.Fatalf("books=%v err=%v", books, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "errCode") {
				t.Fatalf("missing upstream diagnostic: %v", err)
			}
		})
	}
}
