// 文本命令路由与菜单实现。所有数据访问都以「绑定关系」为边界：
// 普通用户只能操作自己绑定的账号；管理员（面板授予）可操作全部。
package wxbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"readtrace/internal/store"
)

// botCtx 是一条微信消息的会话上下文。
type botCtx struct {
	bot          *Bot
	cl           *Client
	ctx          context.Context
	userID       string
	contextToken string
	admin        bool
	binds        []store.Account
}

func (bc *botCtx) reply(msg string) {
	bc.bot.reply(bc.ctx, bc.cl, bc.userID, bc.contextToken, msg)
}

// otpOnly 判断整条文本是否是 4~8 位数字（登录验证码）。
func otpOnly(text string) string {
	s := strings.TrimSpace(text)
	if len(s) < 4 || len(s) > 8 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

var cmdAliases = map[string]string{
	"菜单": "menu", "帮助": "menu", "help": "menu", "?": "menu", "？": "menu", "指令": "menu", "menu": "menu",
	"我的账号": "accounts", "账号": "accounts", "账号列表": "accounts", "accounts": "accounts", "绑定列表": "accounts",
	"开始": "start", "开始阅读": "start", "start": "start", "run": "start",
	"停止": "stop", "stop": "stop",
	"进度": "progress", "状态": "progress", "status": "progress", "progress": "progress",
	"今日": "stats", "今天": "stats", "统计": "stats", "stats": "stats", "today": "stats",
	"最近": "recent", "记录": "recent", "runs": "recent",
	"定时": "sched", "任务": "sched", "schedules": "sched", "sched": "sched", "schedule": "sched",
	"登录": "login", "绑定": "login", "重新绑定": "login", "登录微信读书": "login", "添加账号": "login",
	"扫码登录": "login", "login": "login", "bind": "login", "扫码": "login",
	"解绑": "unbind", "unbind": "unbind",
	"管理员": "admin", "admin": "admin",
}

func normalizeCommand(first string) string {
	key := strings.ToLower(strings.TrimSpace(first))
	if v, ok := cmdAliases[key]; ok {
		return v
	}
	return key
}

// route dispatches one inbound text.
func (b *Bot) route(bc *botCtx, text string, reply func(string)) {
	first, rest := splitFirst(text)
	switch normalizeCommand(first) {
	case "menu":
		reply(menuText)
	case "accounts":
		b.cmdAccounts(bc, reply)
	case "start":
		b.cmdStart(bc, fields(rest), reply)
	case "stop":
		b.cmdStop(bc, fields(rest), reply)
	case "progress":
		b.cmdProgress(bc, reply)
	case "stats":
		b.cmdStats(bc, reply)
	case "recent":
		b.cmdRecent(bc, reply)
	case "sched":
		b.cmdSchedules(bc, fields(rest), reply)
	case "login":
		b.cmdLogin(bc, reply)
	case "unbind":
		b.cmdUnbind(bc, fields(rest), reply)
	case "admin":
		b.cmdAdmin(bc, fields(rest), reply)
	default:
		if len(bc.binds) == 0 && !bc.admin {
			reply("你好，我是「阅迹 · ReadTrace」的微信机器人。\n\n" +
				"回复「菜单」查看全部指令；回复「登录」扫码绑定你的微信读书账号。")
			return
		}
		reply("没有这个指令。回复「菜单」查看全部指令。")
	}
}

const menuText = `📖 阅迹 · 微信机器人
【账号】
· 我的账号 —— 查看绑定的微信读书账号
· 登录 —— 扫码绑定新的微信读书账号
· 解绑 序号 —— 解除对该账号的绑定
【阅读】
· 开始 序号 分钟 —— 立即开始阅读（多账号需序号）
· 停止 序号 —— 停止阅读，缺省为你绑定的全部账号
· 进度 或 状态 —— 阅读中的实时进度
· 今日 / 统计 —— 今天的阅读统计
· 最近 —— 最近的运行记录
【定时】
· 定时 列表 —— 查看定时任务
· 定时 新增 序号 22:30 30 —— 每天 22:30 阅读 30 分钟
· 定时 开/关/删 任务编号 —— 启停或删除任务
扫码登录成功后，账号即绑定到你，只有你能通过微信查看和操作它；管理员不受此限制。`

// cmdAccounts lists the user's bound accounts (admin sees everything).
func (b *Bot) cmdAccounts(bc *botCtx, reply func(string)) {
	accounts := bc.binds
	xAll := false
	if bc.admin {
		if all, err := b.st.ListAccounts(); err == nil {
			accounts = all
			xAll = true
		}
	}
	if len(accounts) == 0 {
		reply("还没有绑定微信读书账号。\n回复「登录」扫码绑定第一个。")
		return
	}
	runmap := b.runningMap()
	var out strings.Builder
	out.WriteString("📖 我的账号：\n")
	for i := range accounts {
		out.WriteString(accountLine(&accounts[i], runmap) + "\n")
	}
	if xAll {
		binds, _ := b.st.ListBotBindings()
		if len(binds) > 0 {
			out.WriteString("\n🔗 绑定关系（账号 ← 微信用户）：\n")
			for _, bd := range binds {
				out.WriteString(fmt.Sprintf("· 账号「%s」 ← %s\n", bd.AccountName, bd.UserID))
			}
		}
	}
	reply(out.String())
}

func (b *Bot) runningMap() map[int64]bool {
	runmap := map[int64]bool{}
	for _, p := range b.eng.Snapshot() {
		runmap[p.AccountID] = true
	}
	return runmap
}

func accountLine(a *store.Account, runmap map[int64]bool) string {
	runFlag := ""
	if runmap[a.ID] {
		runFlag = "（阅读中）"
	}
	statusFlag := ""
	if a.Status != "active" {
		statusFlag = " ⚠️" + wxStatusLabel(a.Status)
	}
	name := a.Name
	if a.Nickname != "" {
		name = a.Nickname
	}
	return fmt.Sprintf("【%d】%s%s%s · 默认 %d 分钟", a.Seq, name, runFlag, statusFlag, a.DefaultMinutes)
}

func wxStatusLabel(s string) string {
	switch s {
	case "active":
		return "在线"
	case "expired":
		return "登录失效"
	default:
		return s
	}
}

// cmdStart starts a reading session: 开始 [序号] [分钟].
func (b *Bot) cmdStart(bc *botCtx, args []string, reply func(string)) {
	acc, minutes, ok := b.resolveTarget(bc, args, reply)
	if !ok || acc == nil {
		return
	}
	run, err := bc.bot.eng.Start(acc, minutes, "wxbot")
	if err != nil {
		reply("启动失败：" + err.Error())
		return
	}
	reply(fmt.Sprintf("🚀 已开始阅读：%s\n目标 %s，运行记录 #%d。\n回复「进度」查看实时进度，回复「停止 %d」提前结束。",
		accountName(acc), humanMinutes(minutes*60), run.ID, acc.Seq))
}

func accountName(a *store.Account) string {
	if a.Nickname != "" {
		return a.Nickname
	}
	return a.Name
}

// cmdStop stops runs; without seq stops all of the sender's owned accounts.
func (b *Bot) cmdStop(bc *botCtx, args []string, reply func(string)) {
	targets := b.candidates(bc)
	if bc.admin && len(args) == 0 {
		// 管理员默认只停自己绑定的账号；要操作别人的账号必须显式指定序号，
		// 否则一条「停止」会把全站所有阅读都停掉。
		targets = bc.binds
	}
	if len(args) > 0 {
		acc := b.bySeq(bc, args[0])
		if acc == nil {
			reply(fmt.Sprintf("未找到序号 %s 对应的账号，回复「我的账号」查看。", args[0]))
			return
		}
		targets = []store.Account{*acc}
	}
	if len(targets) == 0 {
		reply("没有可操作的账号。管理员请指定「停止 <序号>」。")
		return
	}
	snap := bc.bot.eng.Snapshot()
	matched := false
	stopped := 0
	for _, acc := range targets {
		for _, p := range snap {
			if p.AccountID != acc.ID {
				continue
			}
			matched = true
			if err := bc.bot.eng.Stop(p.RunID); err == nil {
				stopped++
			}
		}
	}
	if !matched {
		reply("这些账号当前没有正在进行的阅读。")
		return
	}
	reply(fmt.Sprintf("⏹ 已停止 %d 个阅读任务。", stopped))
}

func (b *Bot) cmdProgress(bc *botCtx, reply func(string)) {
	// 只展示当前发送者（或管理员）有权操作的账号，避免「进度」泄露他人数据。
	scope := map[int64]bool{}
	for _, acc := range b.candidates(bc) {
		scope[acc.ID] = true
	}
	snap := bc.bot.eng.Snapshot()
	var out strings.Builder
	shown := 0
	for _, p := range snap {
		if !scope[p.AccountID] {
			continue
		}
		if shown == 0 {
			out.WriteString("⏱ 正在阅读：\n")
		}
		shown++
		name := p.AccountName
		if name == "" {
			name = fmt.Sprintf("账号 #%d", p.AccountID)
		}
		out.WriteString(fmt.Sprintf("· %s：已读 %d / %s，成功 %d 次、失败 %d 次\n",
			name, p.Elapsed, humanMinutes(p.Target), p.Success, p.Failed))
	}
	if shown == 0 {
		reply("你绑定的账号当前没有在阅读。\n回复「开始 序号 分钟」立即开始。")
		return
	}
	reply(out.String())
}

func (b *Bot) cmdStats(bc *botCtx, reply func(string)) {
	targets := b.candidates(bc)
	if len(targets) == 0 && bc.admin {
		tot, _ := b.st.Totals()
		reply(fmt.Sprintf("📊 全站：累计运行 %d 次，累计阅读 %s。", tot.Runs, humanMinutes(int(tot.Seconds))))
		return
	}
	if len(targets) == 0 {
		reply("先「登录」绑定账号，才有统计可看。")
		return
	}
	loc := b.siteTZ()
	now := time.Now().In(loc)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).Unix()
	stats, err := b.st.StatsByAccount(dayStart)
	if err != nil {
		reply("读取统计失败：" + err.Error())
		return
	}
	byID := map[int64]int64{}
	for _, st := range stats {
		byID[st.AccountID] = st.Seconds
	}
	var out strings.Builder
	out.WriteString("📊 今日统计（时区 " + loc.String() + "）：\n")
	for i := range targets {
		acc := &targets[i]
		tot, _ := b.st.TotalsForAccount(acc.ID)
		out.WriteString(fmt.Sprintf("· 【%d】%s：今日 %s，累计 %s（%d 次）\n",
			acc.Seq, accountName(acc), humanMinutes(int(byID[acc.ID])), humanMinutes(int(tot.Seconds)), tot.Runs))
	}
	reply(out.String())
}

func (b *Bot) cmdRecent(bc *botCtx, reply func(string)) {
	targets := b.candidates(bc)
	if len(targets) == 0 {
		reply("没有绑定账号，无法查看记录。")
		return
	}
	loc := b.siteTZ()
	var out strings.Builder
	out.WriteString("🕘 最近记录：\n")
	for i := range targets {
		if i >= 3 {
			out.WriteString("……更多请在面板「记录」页查看。\n")
			break
		}
		acc := &targets[i]
		runs, err := b.st.ListRuns(acc.ID, 3)
		if err != nil {
			continue
		}
		out.WriteString(fmt.Sprintf("【%d】%s：\n", acc.Seq, accountName(acc)))
		for _, r := range runs {
			line := fmt.Sprintf("· %s %s %s（%s）", fmtTs(r.StartedAt, loc), triggerLabel(r.Trigger), wxStatusLabel(r.Status), humanMinutes(r.ElapsedSeconds))
			if r.Error != "" {
				line += "\n  原因：" + r.Error
			}
			out.WriteString(line + "\n")
		}
	}
	reply(out.String())
}

func triggerLabel(t string) string {
	switch t {
	case "wxbot":
		return "微信"
	case "schedule":
		return "定时"
	default:
		return "手动"
	}
}

// ---- 定时任务 ----

func (b *Bot) cmdSchedules(bc *botCtx, args []string, reply func(string)) {
	if len(args) == 0 || normalizeCommand(args[0]) == "列表" || isListArg(args[0]) {
		b.schedList(bc, reply)
		return
	}
	switch normalizeCommand(args[0]) {
	case "新增", "创建", "add", "new", "create":
		b.schedCreate(bc, args[1:], reply)
	case "开", "启用", "on", "enable":
		b.schedToggle(bc, args[1:], true, reply)
	case "关", "停", "暂停", "disable", "off":
		b.schedToggle(bc, args[1:], false, reply)
	case "删", "删除", "delete", "del", "移除", "remove":
		b.schedDelete(bc, args[1:], reply)
	default:
		reply("定时指令：列表 / 新增 / 开 / 关 / 删\n" +
			"示例：「定时 新增 22:30 30」或「定时 新增 2 22:30 30」（多账号需带序号）")
	}
}

func isListArg(s string) bool {
	switch normalizeCommand(s) {
	case "list", "查看", "list-schedules":
		return true
	}
	return false
}

func (b *Bot) schedList(bc *botCtx, reply func(string)) {
	targets := b.candidates(bc)
	all, err := b.st.ListSchedules()
	if err != nil {
		reply("读取定时任务失败：" + err.Error())
		return
	}
	mine := map[int64]bool{}
	for _, acc := range targets {
		mine[acc.ID] = true
	}
	var out strings.Builder
	shown := 0
	for _, sc := range all {
		if !mine[sc.AccountID] && !bc.admin {
			continue
		}
		shown++
		state := "开"
		if !sc.Enabled {
			state = "关"
		}
		out.WriteString(fmt.Sprintf("⏰ 任务[%d]「%s」%s，时长 %s（%s）", sc.ID, sc.AccountName, cronHuman(sc.Cron), humanMinutes(sc.Minutes*60), state))
		if nx := bc.bot.sch.Next(sc.ID); nx > 0 {
			out.WriteString("，下次 " + fmtTs(nx, b.siteTZ()))
		}
		out.WriteString("\n")
	}
	if shown == 0 {
		reply("还没有定时任务。\n示例：「定时 新增 22:30 30」＝每天 22:30 阅读 30 分钟。")
		return
	}
	reply(out.String())
}

// cronHuman renders a cron as friendly text when it is a daily rule.
func cronHuman(cron string) string {
	parts := strings.Fields(cron)
	if len(parts) == 5 && parts[2] == "*" && parts[3] == "*" && parts[4] == "*" {
		h, errH := strconv.Atoi(parts[1])
		m, errM := strconv.Atoi(parts[0])
		if errH == nil && errM == nil {
			return fmt.Sprintf("每天 %02d:%02d", h, m)
		}
	}
	return cron
}

// schedCreate: 定时 新增 [序号] HH:MM [分钟].
func (b *Bot) schedCreate(bc *botCtx, args []string, reply func(string)) {
	targets := b.candidates(bc)
	if len(targets) == 0 {
		reply("请先「登录」绑定一个微信读书账号，再创建定时任务。")
		return
	}
	acc := &targets[0]
	x := 0
	if len(targets) > 1 {
		if len(args) > 0 {
			if _, err := strconv.Atoi(args[0]); err == nil {
				found := b.bySeq(bc, args[0])
				if found == nil {
					reply(fmt.Sprintf("序号 %s 不在你的账号列表里，回复「我的账号」查看。", args[0]))
					return
				}
				acc = found
				x = 1
			} else {
				reply("你有多个账号，格式：定时 新增 <序号> 22:30 [分钟]")
				return
			}
		} else {
			reply("你有多个账号，格式：定时 新增 <序号> 22:30 [分钟]")
			return
		}
	}
	rest := args[x:]
	if len(rest) == 0 {
		reply("格式：定时 新增 [序号] 22:30 [分钟]\n示例：定时 新增 22:30 30")
		return
	}
	hour, min, err := parseHHMM(rest[0])
	if err != nil {
		reply("时间格式应为 HH:MM，例如：定时 新增 22:30 30")
		return
	}
	minutes := acc.DefaultMinutes
	if minutes <= 0 {
		minutes = 30
	}
	if len(rest) >= 2 {
		n, err := strconv.Atoi(rest[1])
		if err != nil || n <= 0 {
			reply("阅读时长应为正整数分钟，例如：定时 新增 22:30 30")
			return
		}
		if n > 1440 {
			n = 1440
		}
		minutes = n
	}
	cron := fmt.Sprintf("%d %d * * *", min, hour)
	if err := bc.bot.sch.Validate(cron); err != nil {
		reply("生成的 cron 无效：" + err.Error())
		return
	}
	sc := &store.Schedule{
		AccountID: acc.ID,
		Cron:      cron,
		Minutes:   minutes,
		Enabled:   true,
	}
	if err := b.st.CreateSchedule(sc); err != nil {
		reply("创建任务失败：" + err.Error())
		return
	}
	if err := bc.bot.sch.Add(sc); err != nil {
		_ = b.st.DeleteSchedule(sc.ID)
		reply("注册定时失败：" + err.Error())
		return
	}
	preview, _ := bc.bot.sch.Preview(cron, 1)
	next := ""
	if len(preview) > 0 {
		next = "，下次 " + fmtTs(preview[0].Unix(), b.siteTZ())
	}
	reply(fmt.Sprintf("✅ 已为账号【%d】%s 创建每天 %02d:%02d 的阅读任务，时长 %s%s\n任务编号 [%d]，可用「定时 关 %d」暂停，或「定时 删 %d」删除。",
		acc.Seq, accountName(acc), hour, min, humanMinutes(minutes*60), next, sc.ID, sc.ID, sc.ID))
}

func (b *Bot) schedToggle(bc *botCtx, args []string, on bool, reply func(string)) {
	done, denied := b.schedOperate(bc, args, func(sc *store.Schedule) error {
		if err := b.st.SetScheduleEnabled(sc.ID, on); err != nil {
			return err
		}
		if on {
			return bc.bot.sch.Add(sc)
		}
		bc.bot.sch.Remove(sc.ID)
		return nil
	}, reply)
	if denied {
		return
	}
	if done == 0 {
		reply("没有匹配的任务编号，请用「定时 列表」查看。")
		return
	}
	if on {
		reply(fmt.Sprintf("✅ 已启用 %d 个任务。", done))
	} else {
		reply(fmt.Sprintf("⏸ 已暂停 %d 个任务。", done))
	}
}

func (b *Bot) schedDelete(bc *botCtx, args []string, reply func(string)) {
	done, denied := b.schedOperate(bc, args, func(sc *store.Schedule) error {
		bc.bot.sch.Remove(sc.ID)
		return b.st.DeleteSchedule(sc.ID)
	}, reply)
	if denied {
		return
	}
	if done == 0 {
		reply("没有匹配的任务编号，请用「定时 列表」查看。")
		return
	}
	reply(fmt.Sprintf("✅ 已删除 %d 个任务。", done))
}

// schedOperate validates ownership then applies fn to each requested schedule.
func (b *Bot) schedOperate(bc *botCtx, args []string, fn func(sc *store.Schedule) error, reply func(string)) (done int, denied bool) {
	targets := b.candidates(bc)
	mine := map[int64]bool{}
	for _, acc := range targets {
		mine[acc.ID] = true
	}
	for _, a := range args {
		id, err := strconv.ParseInt(strings.Trim(a, "[]"), 10, 64)
		if err != nil {
			continue
		}
		sc, err := b.st.GetSchedule(id)
		if err != nil || sc == nil {
			continue
		}
		if !mine[sc.AccountID] && !bc.admin {
			reply(fmt.Sprintf("任务 [%d] 不属于你绑定的账号（账号「%s」），无法操作。", sc.ID, sc.AccountName))
			return 0, true
		}
		if err := fn(sc); err == nil {
			done++
		}
	}
	return done, false
}

// ---- 解绑 / 管理员 ----

func (b *Bot) cmdUnbind(bc *botCtx, args []string, reply func(string)) {
	if len(args) == 0 {
		reply("格式：解绑 <账号序号>，例如「解绑 0」。\n回复「我的账号」查看序号。")
		return
	}
	acc := b.bySeq(bc, args[0])
	if acc == nil {
		reply("未找到该序号对应的绑定账号。")
		return
	}
	del, err := b.st.UnbindUserAccount(bc.userID, acc.ID)
	if err != nil {
		reply("解绑失败：" + err.Error())
		return
	}
	if !del {
		reply("该账号并不绑定在你这里。")
		return
	}
	reply(fmt.Sprintf("已解除账号【%d】%s 的绑定。该账号仍在面板中，可被管理员重新绑定。", acc.Seq, accountName(acc)))
}

func (b *Bot) cmdAdmin(bc *botCtx, args []string, reply func(string)) {
	if !bc.admin {
		reply("仅管理员可用。管理员权限由面板「微信 → 用户与账号」授予。")
		return
	}
	if len(args) == 0 {
		reply("管理员指令：\n· 管理员 账号 —— 全部账号与绑定关系\n· 管理员 解绑 <账号序号> —— 清空该账号的微信绑定\n更多管理（授权/开关）请在面板「微信」页操作。")
		return
	}
	switch normalizeCommand(args[0]) {
	case "账号", "accounts", "列表":
		b.cmdAccounts(bc, reply)
	case "解绑", "unbind":
		if len(args) < 2 {
			reply("格式：管理员 解绑 <账号序号>")
			return
		}
		acc := b.bySeq(bc, args[1])
		if acc == nil {
			reply("未找到该序号。")
			return
		}
		if err := b.st.UnbindAccount(acc.ID); err != nil {
			reply("解绑失败：" + err.Error())
			return
		}
		reply(fmt.Sprintf("已清空账号【%d】%s 的全部微信绑定。", acc.Seq, accountName(acc)))
	default:
		reply("管理员指令见「管理员」。")
	}
}

// ---- 目标解析 ----

// candidates returns the accounts the sender may operate. Admins may operate
// every account (the panel shows them all), regular users only their bindings.
func (b *Bot) candidates(bc *botCtx) []store.Account {
	if bc.admin {
		if all, err := b.st.ListAccounts(); err == nil {
			return all
		}
	}
	return bc.binds
}

// bySeq looks up an account by display seq within the sender's scope.
func (b *Bot) bySeq(bc *botCtx, token string) *store.Account {
	n, err := strconv.Atoi(strings.TrimSpace(token))
	if err != nil {
		return nil
	}
	account := b.candidates(bc)
	for i := range account {
		if account[i].Seq == int64(n) {
			return &account[i]
		}
	}
	return nil
}

// resolveTarget parses "开始 [序号] [分钟]" into account + minutes.
func (b *Bot) resolveTarget(bc *botCtx, args []string, reply func(string)) (*store.Account, int, bool) {
	binds := b.candidates(bc)
	nums := []int{}
	for _, a := range args {
		if n, err := strconv.Atoi(a); err == nil && n >= 0 {
			nums = append(nums, n)
		}
	}
	if len(binds) == 0 {
		reply("你还没有绑定微信读书账号，回复「登录」扫码绑定。")
		return nil, 0, true
	}
	switch {
	case len(nums) >= 2:
		acc := findBySeq(binds, nums[0])
		if acc == nil {
			reply(fmt.Sprintf("序号 %d 不在你的账号列表里，回复「我的账号」查看。", nums[0]))
			return nil, 0, true
		}
		m := nums[1]
		if m <= 0 || m > 1440 {
			m = acc.DefaultMinutes
		}
		return acc, m, true
	case len(nums) == 1 && len(binds) == 1:
		acc := &binds[0]
		m := nums[0]
		if m <= 0 || m > 1440 {
			m = acc.DefaultMinutes
		}
		return acc, m, true
	case len(nums) == 1:
		acc := findBySeq(binds, nums[0])
		if acc == nil {
			reply(fmt.Sprintf("序号 %d 不在你的账号列表里，回复「我的账号」查看。", nums[0]))
			return nil, 0, true
		}
		return acc, acc.DefaultMinutes, true
	case len(binds) == 1:
		acc := &binds[0]
		return acc, acc.DefaultMinutes, true
	default:
		var out strings.Builder
		out.WriteString("你有多个账号，格式：开始 <序号> [分钟]\n")
		for i := range binds {
			out.WriteString(fmt.Sprintf("【%d】%s\n", binds[i].Seq, accountName(&binds[i])))
		}
		reply(out.String())
		return nil, 0, true
	}
}

func findBySeq(list []store.Account, n int) *store.Account {
	for i := range list {
		if list[i].Seq == int64(n) {
			return &list[i]
		}
	}
	return nil
}

// splitFirst splits the first token from rest.
func splitFirst(text string) (string, string) {
	for i := 0; i < len(text); i++ {
		if text[i] == ' ' || text[i] == '\t' {
			return text[:i], strings.TrimSpace(text[i:])
		}
	}
	return text, ""
}

func fields(s string) []string { return strings.Fields(s) }

func parseHHMM(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want HH:MM")
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("时间需在 00:00-23:59 之间")
	}
	return h, m, nil
}

func fmtTs(ts int64, loc *time.Location) string {
	return time.Unix(ts, 0).In(loc).Format("01-02 15:04")
}
