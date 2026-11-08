package wxbot

import (
	"log/slog"
	"path/filepath"
	"testing"

	"readtrace/internal/secure"
	"readtrace/internal/store"
)

func newBotTestStore(t *testing.T) (*store.Store, *Bot) {
	t.Helper()
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatalf("secure.New: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"), box)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, New(st, nil, nil, nil, nil, slog.Default())
}

func TestEnsureOwnerAdmin(t *testing.T) {
	st, b := newBotTestStore(t)
	if err := st.SaveBotLogin("bot-1", "wx-owner", "token", ""); err != nil {
		t.Fatalf("SaveBotLogin: %v", err)
	}
	b.EnsureOwnerAdmin()
	u, err := st.GetBotUser("wx-owner")
	if err != nil || u == nil || !u.IsAdmin {
		t.Fatalf("owner should be admin: %v %v", u, err)
	}
}

func TestCandidatesAdminScope(t *testing.T) {
	st, b := newBotTestStore(t)
	a1, err := st.CreateAccount(store.Account{Name: "一号", Vid: "v1"}, nil, nil)
	if err != nil {
		t.Fatalf("create a1: %v", err)
	}
	if _, err := st.CreateAccount(store.Account{Name: "二号", Vid: "v2"}, nil, nil); err != nil {
		t.Fatalf("create a2: %v", err)
	}
	if _, err := st.BindUserAccount("wx-a", a1); err != nil {
		t.Fatalf("bind: %v", err)
	}
	binds, err := st.UserBoundAccounts("wx-a")
	if err != nil {
		t.Fatalf("UserBoundAccounts: %v", err)
	}

	user := &botCtx{bot: b, userID: "wx-a", binds: binds}
	if got := b.candidates(user); len(got) != 1 || got[0].ID != a1 {
		t.Fatalf("regular user candidates = %v", got)
	}

	admin := &botCtx{bot: b, userID: "wx-a", admin: true, binds: binds}
	if got := b.candidates(admin); len(got) != 2 {
		t.Fatalf("admin candidates = %v, want all accounts", got)
	}
}
