package api

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/sched"
	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

func TestAccountDeleteKeepsLiveSchedulesWhenTransactionFails(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", failDelete), func(t *testing.T) {
			dir := t.TempDir()
			box, err := secure.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "test.db")
			st, err := store.Open(path, box)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			eng := engine.New(st, weread.NewClient(), nil, engine.NewHub(), log)
			sch := sched.New(st, eng, log)
			s := &Server{st: st, eng: eng, sch: sch, log: log}
			id, err := st.CreateAccount(store.Account{Name: "delete"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddPoolItem(store.KindBook, "book", "book", id); err != nil {
				t.Fatal(err)
			}
			v := &store.Schedule{AccountID: id, Cron: "0 0 * * *", Minutes: 1, Enabled: true}
			if err := st.CreateSchedule(v); err != nil {
				t.Fatal(err)
			}
			if err := sch.Start(time.UTC); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sch.Stop)
			before := sch.Next(v.ID)
			if before == 0 {
				t.Fatal("schedule was not registered")
			}
			if failDelete {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`CREATE TRIGGER fail_account_pool_delete BEFORE DELETE ON pool_items
					BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodDelete, "/", nil)
			r.SetPathValue("id", fmt.Sprint(id))
			w := httptest.NewRecorder()
			s.handleAccountDelete(w, r)
			acc, err := st.GetAccount(id)
			if err != nil {
				t.Fatal(err)
			}
			if failDelete {
				if w.Code != http.StatusInternalServerError || acc == nil || sch.Next(v.ID) != before {
					t.Fatalf("failed delete disabled live schedule: status=%d account=%v next=%d", w.Code, acc, sch.Next(v.ID))
				}
			} else if w.Code != http.StatusOK || acc != nil || sch.Next(v.ID) != 0 {
				t.Fatalf("successful delete left schedule: status=%d account=%v next=%d", w.Code, acc, sch.Next(v.ID))
			}
		})
	}
}
