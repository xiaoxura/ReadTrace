// 绑定关系数据层测试：单所有者约束、解绑、级联删除。
package store

import (
	"testing"
	"time"
)

func TestBotLoginRoundTrip(t *testing.T) {
	st := newTestStore(t)
	if login, err := st.GetBotLogin(); err != nil || login != nil {
		t.Fatalf("empty login: %v %v", login, err)
	}
	if err := st.SaveBotLogin("bot1", "user1", "tok-abc", ""); err != nil {
		t.Fatalf("save login: %v", err)
	}
	// 空 token 保留原凭据，baseURL 才是第二个维度。
	if err := st.SaveBotLogin("bot1b", "user1b", "", "https://x.example"); err != nil {
		t.Fatalf("resave login: %v", err)
	}
	login, err := st.GetBotLogin()
	if err != nil || login == nil || !login.HasToken {
		t.Fatalf("login should persist: %v %v", login, err)
	}
	if login.BotID != "bot1b" {
		t.Fatalf("bot id = %s, want bot1b", login.BotID)
	}
	if login.BaseURL != "https://x.example" {
		t.Fatalf("baseURL = %s", login.BaseURL)
	}
	if tok, err := st.BotToken(); err != nil || tok != "tok-abc" {
		t.Fatalf("token = %q %v, want tok-abc", tok, err)
	}
	if err := st.SaveBotLogin("bot2", "user2", "", ""); err != nil {
		t.Fatalf("save empty token: %v", err)
	}
	if tok, _ := st.BotToken(); tok != "tok-abc" {
		t.Fatalf("empty save must keep token, got %q", tok)
	}
	// 游标持久化
	if err := st.SaveBotUpdatesBuf("cursor-b64"); err != nil {
		t.Fatalf("save buf: %v", err)
	}
	if buf, _ := st.BotUpdatesBuf(); buf != "cursor-b64" {
		t.Fatalf("buf = %q", buf)
	}
	if err := st.ClearBotLogin(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if login, _ := st.GetBotLogin(); login != nil {
		t.Fatalf("login should be gone")
	}
	if buf, _ := st.BotUpdatesBuf(); buf != "" {
		t.Fatalf("buf should be gone, got %q", buf)
	}
}

func TestBindUserAccountSingleOwner(t *testing.T) {
	st := newTestStore(t)
	accID, err := st.CreateAccount(Account{Name: "阅读一号", Vid: "v1"}, nil, nil)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	if _, err := st.BindUserAccount("wx-a", accID); err != nil {
		t.Fatalf("bind a: %v", err)
	}
	owner, err := st.AccountOwner(accID)
	if err != nil || owner != "wx-a" {
		t.Fatalf("owner = %q %v", owner, err)
	}

	// 另一个用户扫码同一个账号 → 接管所有权，唯一所有者。
	if _, err := st.BindUserAccount("wx-b", accID); err != nil {
		t.Fatalf("bind b: %v", err)
	}
	ids, err := st.UserBoundAccountIDs("wx-b")
	if err != nil || len(ids) != 1 || ids[0] != accID {
		t.Fatalf("wx-b bindings = %v %v", ids, err)
	}
	if ids, _ := st.UserBoundAccountIDs("wx-a"); len(ids) != 0 {
		t.Fatalf("wx-a should lose ownership, ids = %v", ids)
	}

	// 一个用户可以绑定多个账号。
	acc2, err := st.CreateAccount(Account{Name: "阅读二号", Vid: "v2"}, nil, nil)
	if err != nil {
		t.Fatalf("create acc2: %v", err)
	}
	if _, err := st.BindUserAccount("wx-b", acc2); err != nil {
		t.Fatalf("bind b2: %v", err)
	}
	if ids, _ := st.UserBoundAccountIDs("wx-b"); len(ids) != 2 {
		t.Fatalf("wx-b should own 2 accounts, got %v", ids)
	}
	accs, err := st.UserBoundAccounts("wx-b")
	if err != nil || len(accs) != 2 {
		t.Fatalf("UserBoundAccounts = %v %v", accs, err)
	}

	// 解绑单个关系。
	ok, err := st.UnbindUserAccount("wx-b", acc2)
	if err != nil || !ok {
		t.Fatalf("unbind b2: %v %v", ok, err)
	}
	if ok, _ := st.UnbindUserAccount("wx-b", acc2); ok {
		t.Fatalf("second unbind should be no-op")
	}

	// 管理员整账号解绑。
	if err := st.UnbindAccount(accID); err != nil {
		t.Fatalf("unbind account: %v", err)
	}
	if owner, _ := st.AccountOwner(accID); owner != "" {
		t.Fatalf("owner should be cleared, got %q", owner)
	}
}

func TestDeleteAccountCascadesBindings(t *testing.T) {
	st := newTestStore(t)
	accID, err := st.CreateAccount(Account{Name: "a", Vid: "v1"}, nil, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.BindUserAccount("wx-a", accID); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := st.DeleteAccount(accID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ids, _ := st.UserBoundAccountIDs("wx-a"); len(ids) != 0 {
		t.Fatalf("bindings should cascade, got %v", ids)
	}
}

func TestBotUsersAdmin(t *testing.T) {
	st := newTestStore(t)
	if err := st.EnsureBotUser("wx-a"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := st.EnsureBotUser("wx-a"); err != nil {
		t.Fatalf("ensure twice: %v", err)
	}
	u, err := st.GetBotUser("wx-a")
	if err != nil || u == nil || u.IsAdmin {
		t.Fatalf("user = %v %v", u, err)
	}
	if err := st.SetBotUserAdmin("wx-a", true); err != nil {
		t.Fatalf("set admin: %v", err)
	}
	if u, _ := st.GetBotUser("wx-a"); u == nil || !u.IsAdmin {
		t.Fatalf("admin flag missing")
	}
	users, err := st.ListBotUsers()
	if err != nil || len(users) != 1 {
		t.Fatalf("list users = %v %v", users, err)
	}
	if err := st.DeleteBotUser("wx-a"); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if u, _ := st.GetBotUser("wx-a"); u != nil {
		t.Fatalf("user should be gone")
	}
}

func TestBotMessagesRecordListPurge(t *testing.T) {
	st := newTestStore(t)
	_, _ = st.RecordBotMessage("wx-a", "in", "text", "开始 0 30", "m1")
	_, _ = st.RecordBotMessage("wx-a", "out", "text", "已开始阅读", "")
	_, _ = st.RecordBotMessage("wx-a", "in", "image", "[图片]", "m2")
	if _, err := st.RecordBotMessage("wx-a", "in", "text", "", "empty"); err != nil {
		t.Fatalf("empty record: %v", err)
	}
	msgs, err := st.ListBotMessages("wx-a", 0, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	// 倒序：最新在前。
	if msgs[0].ID < msgs[2].ID {
		t.Fatalf("messages not desc: %d %d", msgs[0].ID, msgs[2].ID)
	}
	older, err := st.ListBotMessages("wx-a", msgs[1].ID, 50)
	if err != nil || len(older) != 1 {
		t.Fatalf("before_id page = %v %v", older, err)
	}
	if _, err := st.ListBotMessages("wx-b", 0, 50); err != nil {
		t.Fatalf("other user list: %v", err)
	}
	// 保留策略：老消息随停放清理。
	old := time.Now().AddDate(0, 0, -10).Unix()
	if _, err := st.db.Exec(`UPDATE bot_messages SET ts = ?`, old); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, err := st.PurgeHistory(7); err != nil {
		t.Fatalf("purge: %v", err)
	}
	again, _ := st.ListBotMessages("wx-a", 0, 50)
	if len(again) != 0 {
		t.Fatalf("messages should be purged, got %d", len(again))
	}
}
