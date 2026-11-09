package engine

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"readtrace/internal/store"
	"readtrace/internal/weread"
)

func TestCustomPageProgressCommitsOnlyAfterSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := testEngineStore(t)
		id, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom", BookID: "book"},
			map[string]string{"wr_skey": "old"}, map[string]any{"b": "book", "c": "chapter", "co": 10, "pr": 10})
		if err != nil {
			t.Fatal(err)
		}
		acc, err := st.GetAccount(id)
		if err != nil {
			t.Fatal(err)
		}
		client := weread.NewClient()
		var pages []struct{ co, pr int }
		client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, headers := `{"succ":1,"synckey":1}`, http.Header{}
			if r.URL.Path == "/web/login/renewal" {
				headers.Set("Set-Cookie", "wr_skey=new")
			} else {
				var p map[string]any
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Fatal(err)
				}
				pages = append(pages, struct{ co, pr int }{int(p["co"].(float64)), int(p["pr"].(float64))})
				if len(pages) == 1 {
					body = `{"succ":0,"errCode":57}`
				}
			}
			return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		eng := New(st, client, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		run, err := eng.Start(acc, 1, "manual")
		if err != nil {
			t.Fatal(err)
		}
		defer eng.StopAll()
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		got, err := st.GetRun(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "success" || len(pages) != 3 {
			t.Fatalf("run=%+v pages=%+v", got, pages)
		}
		if pages[0] != pages[1] || pages[2].pr != pages[1].pr+1 || pages[2].co <= pages[1].co {
			t.Fatalf("failed page was committed or success did not advance: %+v", pages)
		}
	})
}

func TestTransientRenewalAfterRead401DoesNotExpireAccount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := testEngineStore(t)
		id, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom", BookID: "book"},
			map[string]string{"wr_skey": "old"}, map[string]any{"b": "book", "c": "chapter"})
		if err != nil {
			t.Fatal(err)
		}
		acc, err := st.GetAccount(id)
		if err != nil {
			t.Fatal(err)
		}
		acc.MaxRetries = 0
		if err := st.UpdateAccount(*acc); err != nil {
			t.Fatal(err)
		}
		client := weread.NewClient()
		client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			status, body := http.StatusUnauthorized, "unauthorized"
			if r.URL.Path == "/web/login/renewal" {
				status, body = http.StatusServiceUnavailable, "temporary"
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		eng := New(st, client, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		run, err := eng.Start(acc, 1, "manual")
		if err != nil {
			t.Fatal(err)
		}
		defer eng.StopAll()
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		got, err := st.GetRun(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		status, err := st.GetAccount(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "failed" || status.Status != "active" {
			t.Fatalf("run=%+v account=%+v", got, status)
		}
	})
}
