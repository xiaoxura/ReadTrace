package store

import (
	"fmt"
	"testing"
)

func TestDeleteAccountClearsOnlyOwnedPoolAndSequenceProgress(t *testing.T) {
	st := newTestStore(t)
	first := seedAccount(t, st, "first")
	second := seedAccount(t, st, "second")
	for _, accountID := range []int64{first, second} {
		if _, err := st.AddPoolItem(KindBook, "same-book", "book", accountID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddChapterItem(accountID, "same-book", fmt.Sprintf("chapter-%d", accountID), "chapter", 100); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSetting(fmt.Sprintf("sequence_pos_%d", accountID), `{"chapter":1}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteAccount(first); err != nil {
		t.Fatal(err)
	}
	var firstPool, secondPool int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE account_id = ?`, first).Scan(&firstPool); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE account_id = ?`, second).Scan(&secondPool); err != nil {
		t.Fatal(err)
	}
	if firstPool != 0 || secondPool != 2 {
		t.Fatalf("pool rows after deletion: first=%d second=%d", firstPool, secondPool)
	}
	firstProgress, err := st.GetSetting(fmt.Sprintf("sequence_pos_%d", first))
	if err != nil || firstProgress != "" {
		t.Fatalf("deleted account progress = %q, err = %v", firstProgress, err)
	}
	secondProgress, err := st.GetSetting(fmt.Sprintf("sequence_pos_%d", second))
	if err != nil || secondProgress == "" {
		t.Fatalf("other account progress = %q, err = %v", secondProgress, err)
	}
	if got, err := st.GetAccount(second); err != nil || got == nil {
		t.Fatalf("other account was changed: %+v, %v", got, err)
	}
}

func TestDeleteAccountPoolFailureRollsBackAllCleanup(t *testing.T) {
	st := newTestStore(t)
	accountID := seedAccount(t, st, "first")
	if _, err := st.AddPoolItem(KindBook, "book", "book", accountID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(fmt.Sprintf("sequence_pos_%d", accountID), "progress"); err != nil {
		t.Fatal(err)
	}
	run := &Run{AccountID: accountID, Trigger: "manual", PlanSeconds: 60,
		ElapsedSeconds: 60, Status: "success", StartedAt: 100, FinishedAt: 160}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fail_pool_delete BEFORE DELETE ON pool_items
		BEGIN SELECT RAISE(ABORT, 'deliberate account deletion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAccount(accountID); err == nil {
		t.Fatal("pool deletion failure should abort account deletion")
	}
	if got, err := st.GetAccount(accountID); err != nil || got == nil {
		t.Fatalf("account after failed deletion = %+v, %v", got, err)
	}
	if got, err := st.GetRun(run.ID); err != nil || got == nil {
		t.Fatalf("run after failed deletion = %+v, %v", got, err)
	}
	if value, err := st.GetSetting(fmt.Sprintf("sequence_pos_%d", accountID)); err != nil || value != "progress" {
		t.Fatalf("progress after failed deletion = %q, %v", value, err)
	}
}
