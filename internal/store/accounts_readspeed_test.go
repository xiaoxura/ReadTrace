package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"readtrace/internal/secure"
)

func TestClampReadSpeed(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 300},  // 未填 = 默认
		{-5, 300}, // 非法回落默认
		{1, 1},    // 下限以上原样保留（下限校验在 API）
		{10, 10},
		{300, 300},
		{2999, 2999},
		{3000, 3000},
		{3001, 3000}, // 上限 clamp
		{99999, 3000},
	}
	for _, c := range cases {
		if got := clampReadSpeed(c.in); got != c.want {
			t.Fatalf("clampReadSpeed(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// 读写全链路：CreateAccount 落库、GetAccount 扫描、UpdateAccount 更新都带
// read_speed，且 0/超限值在写入前折算。
func TestReadSpeedCreateUpdateRoundTrip(t *testing.T) {
	st := newTestStore(t)

	id, err := st.CreateAccount(Account{Name: "speedy", ReadSpeed: 600}, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	acc, err := st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("get: %v, %v", acc, err)
	}
	if acc.ReadSpeed != 600 {
		t.Fatalf("CreateAccount read_speed = %d, want 600", acc.ReadSpeed)
	}

	// 新建未填 → 默认 300。
	id2, err := st.CreateAccount(Account{Name: "default"}, nil, nil)
	if err != nil {
		t.Fatalf("create default: %v", err)
	}
	if acc2, _ := st.GetAccount(id2); acc2.ReadSpeed != 300 {
		t.Fatalf("default read_speed = %d, want 300", acc2.ReadSpeed)
	}

	// 更新：0 → 默认 300；超限 → 3000；正常值原样。
	a := Account{ID: id, Name: "speedy"}
	a.ReadSpeed = 0
	if err := st.UpdateAccount(a); err != nil {
		t.Fatalf("update 0: %v", err)
	}
	if acc, _ = st.GetAccount(id); acc.ReadSpeed != 300 {
		t.Fatalf("after 0 update read_speed = %d, want 300", acc.ReadSpeed)
	}
	a.ReadSpeed = 9999
	if err := st.UpdateAccount(a); err != nil {
		t.Fatalf("update 9999: %v", err)
	}
	if acc, _ = st.GetAccount(id); acc.ReadSpeed != 3000 {
		t.Fatalf("after 9999 update read_speed = %d, want 3000", acc.ReadSpeed)
	}
	a.ReadSpeed = 1200
	if err := st.UpdateAccount(a); err != nil {
		t.Fatalf("update 1200: %v", err)
	}
	if acc, _ = st.GetAccount(id); acc.ReadSpeed != 1200 {
		t.Fatalf("after 1200 update read_speed = %d, want 1200", acc.ReadSpeed)
	}
}

// FindAccountByVid 走 stats.go 的独立扫描路径，也要带出 read_speed。
func TestReadSpeedViaVidLookup(t *testing.T) {
	st := newTestStore(t)
	id, err := st.CreateAccount(Account{Name: "v", Vid: "vid1", ReadSpeed: 900}, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	acc, err := st.FindAccountByVid("vid1")
	if err != nil || acc == nil || acc.ID != id {
		t.Fatalf("find by vid: %v, %v", acc, err)
	}
	if acc.ReadSpeed != 900 {
		t.Fatalf("vid lookup read_speed = %d, want 900", acc.ReadSpeed)
	}
}

// 老库（无 read_speed 列）升级：迁移补列，模型未填的既有账号读出默认 300。
func TestMigrateLegacyAccountsReadSpeedDefaults(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	// SQLite ≥ 3.35：模拟老库直接把新列摘掉，留下无 read_speed 的表。
	if _, err := db.Exec(`ALTER TABLE accounts DROP COLUMN read_speed`); err != nil {
		db.Close()
		t.Skipf("SQLite 不支持 DROP COLUMN，跳过老库模拟: %v", err)
	}
	// 一列现网账号（迁移时 supposing read_speed DEFAULT 300 生效）。
	if _, err := db.Exec(`INSERT INTO accounts (seq, name, status, retry_delay, mode, created_at, updated_at)
		VALUES (0, '老账号', 'active', 15, 'sequence', 1, 1)`); err != nil {
		t.Fatalf("seed legacy account: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatalf("migrate legacy accounts: %v", err)
	}
	defer st.Close()
	acc, err := st.GetAccount(1)
	if err != nil || acc == nil {
		t.Fatalf("get after migration: %v, %v", acc, err)
	}
	if acc.ReadSpeed != 300 {
		t.Fatalf("legacy account read_speed = %d, want default 300", acc.ReadSpeed)
	}
}
