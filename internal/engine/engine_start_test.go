package engine

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"readtrace/internal/store"
	"readtrace/internal/weread"
)

func TestStartValidationDoesNotCreateRunningRun(t *testing.T) {
	st := testEngineStore(t)
	id, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom"},
		map[string]string{"wr_skey": "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, nil, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	acc, err := st.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(acc, 1, "manual"); err == nil {
		t.Fatal("missing template accepted")
	}
	if err := st.SetSetting(store.KeyDefaultReadPayload, `{"b":"book","c":"chapter"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(acc, 1, "manual"); err == nil {
		t.Fatal("missing custom book accepted")
	}
	acc.Mode = "rotate"
	if _, err := e.Start(acc, 1, "manual"); err == nil {
		t.Fatal("empty book pool accepted")
	}
	runs, err := st.ListRuns(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 || e.IsRunning(id) {
		t.Fatalf("validation failure left a run: %+v", runs)
	}
}

func TestStartIgnoresUnlinkedChaptersAndBooksWithoutChapters(t *testing.T) {
	for _, mode := range []string{"rotate", "sequence"} {
		t.Run(mode, func(t *testing.T) {
			st := testEngineStore(t)
			id, err := st.CreateAccount(store.Account{Name: "test", Mode: mode},
				map[string]string{"wr_skey": "key"}, map[string]any{"b": "template-book", "c": "template-chapter"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddPoolItem(store.KindBook, "book1", "Book 1", id); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddPoolItem(store.KindChapter, "legacy-unlinked", "Legacy", id); err != nil {
				t.Fatal(err)
			}
			acc, err := st.GetAccount(id)
			if err != nil {
				t.Fatal(err)
			}
			client := weread.NewClient()
			client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				headers := http.Header{}
				if r.URL.Path == "/web/login/renewal" {
					headers.Set("Set-Cookie", "wr_skey=new")
				}
				return &http.Response{StatusCode: 200, Header: headers,
					Body: io.NopCloser(strings.NewReader(`{"succ":1,"synckey":1}`))}, nil
			})
			e := New(st, client, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if _, err := e.Start(acc, 1, "manual"); err == nil {
				t.Fatal("unlinked legacy chapter made book readable")
			}
			runs, err := st.ListRuns(id, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 0 {
				t.Fatalf("invalid pool left runs: %+v", runs)
			}
			if _, err := st.AddChapterItem(id, "book1", "chapter1", "Chapter 1", 300); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddPoolItem(store.KindBook, "book2", "Book 2", id); err != nil {
				t.Fatal(err)
			}
			run, err := e.Start(acc, 1, "manual")
			if err != nil {
				t.Fatal(err)
			}
			defer e.StopAll()
			e.mu.Lock()
			ar := e.act[run.ID]
			e.mu.Unlock()
			if ar == nil {
				t.Fatal("run disappeared before inspection")
			}
			if len(ar.poolBooksOwned) != 1 || ar.poolBooksOwned[0] != "book1" {
				t.Fatalf("eligible books = %+v", ar.poolBooksOwned)
			}
			if mode == "sequence" && (len(ar.orderedBookList) != 1 || ar.orderedBookList[0] != "book1") {
				t.Fatalf("sequence includes book without chapters: %+v", ar.orderedBookList)
			}
		})
	}
}
