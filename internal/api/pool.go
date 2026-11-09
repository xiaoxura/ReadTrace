package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// ---------- 书池与章节池管理 ----------

// authedCall 执行一次 weread 调用；如果失败原因是登录失效（-2012/-2013/401
// 等），先做 renewal 拿新 wr_skey（Renew 会更新 ck 本身，后续调用方继续用的
// 就是新 Cookie）再重试一次，与阅读引擎的自愈语义保持一致。此前书架/导入
// 这些 API 通道没有续期，Cookie 自然过期后第一次操作会直接报「登录超时」，
// 用户被迫先去点一次「检测」。
func (s *Server) authedCall(ctx context.Context, acc *store.Account, ck map[string]string, fn func() error) error {
	err := fn()
	if !weread.IsAuthFailure(err) {
		return err
	}
	// 缺陷 03：expectedSkey 取请求开始时的值，写在 Renew 之前（Renew 成功
	// 时会原地更新 ck）。Renew 可能要等很久，等待期间用户可能扫码/导入新
	// 凭据；写回前用 CAS 比对库里仍是 expectedSkey 才落库。
	expectedSkey := ck["wr_skey"]
	key, renewErr := s.wc.Renew(ctx, ck)
	if renewErr != nil {
		return err // 续期失败：返回原始错误，不掩盖原因
	}
	if key == "" {
		return err
	}
	written, saveErr := s.st.SaveCookiesIfCurrentSkey(acc.ID, expectedSkey, ck)
	switch {
	case saveErr != nil:
		s.log.Warn("续期结果写回失败", "account", acc.ID, "err", saveErr)
	case !written:
		s.log.Info("续期等待期间凭据已变更（新登录/换绑），跳过本次写回，不覆盖新凭据",
			"account", acc.ID)
	}
	return fn()
}

// BookAudit 是一本书的章节付费结构快照（账号视角）。
type BookAudit struct {
	Total  int `json:"total"`  // 目录章节数
	Priced int `json:"priced"` // 付费未购章节数（paid=0 且 price≠0，官方接口对未购章节返回 price=-1）
	Owned  int `json:"owned"`  // 已解锁章节数（price>0 且 paid=1）
}

// label 判定语义：
//   - Locked==Total → 未付费（全书都要钱且一章没买）
//   - Locked>0     → 部分付费（仍有 N 章未购）
//   - Locked==0 && Owned>0 → 已付费
//   - 其他           → 免费
func (a BookAudit) Label() (string, string) {
	cls, text := "", ""
	switch {
	case a.Total == 0:
		text, cls = "未知", "neutral"
	case a.Priced == a.Total:
		text, cls = "未付费", "err"
	case a.Priced > 0:
		text, cls = fmt.Sprintf("部分付费(未购 %d 章)", a.Priced), "warn"
	case a.Owned > 0:
		text, cls = "已付费", "ok"
	default:
		text, cls = "免费", "ok"
	}
	return text, cls
}

// auditBooks 并发拉取本页书籍目录，统计每本书的付费结构。返回（审计结果，
// 每本审计失败的错误信息）——失败的书在结果里缺省，调用方可以展示原因。
func (s *Server) auditBooks(ctx context.Context, accountID int64, ck map[string]string, books []weread.Book) (map[string]BookAudit, map[string]string) {
	out := map[string]BookAudit{}
	errs := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for _, b := range books {
		wg.Add(1)
		sem <- struct{}{}
		go func(bid string) {
			defer wg.Done()
			defer func() { <-sem }()
			chs, err := s.chapterInfos(ctx, accountID, ck, bid)
			if err != nil {
				// 审计失败不阻塞主流程，但把原因带给调用方，不让用户对着
				// 满屏「未知」猜是不是坏了。
				mu.Lock()
				errs[bid] = err.Error()
				mu.Unlock()
				return
			}
			a := BookAudit{Total: len(chs)}
			for _, ch := range chs {
				switch {
				case ch.Price > 0 && ch.Paid != 0:
					a.Owned++
				case ch.Unpaid():
					a.Priced++
				}
			}
			mu.Lock()
			out[bid] = a
			mu.Unlock()
		}(b.ID)
	}
	wg.Wait()
	return out, errs
}

// handlePoolShelf 通过协议拉取某个账号的微信读书书架：全部书籍（含归档分
// 组）+ 官方 bookCount。付费结构不在响应里，由 POST /api/pool/audit 按页
// 懒加载，避免整架书架的目录请求拖慢首次拉取。
func (s *Server) handlePoolShelf(w http.ResponseWriter, r *http.Request) {
	archiveOffset := 0
	if raw := r.URL.Query().Get("archive_offset"); raw != "" {
		var err error
		archiveOffset, err = strconv.Atoi(raw)
		if err != nil || archiveOffset < 0 {
			s.writeErr(w, http.StatusBadRequest, "无效的归档分页位置")
			return
		}
	}
	id, _ := parseInt64(r.URL.Query().Get("account_id"))
	acc, err := s.resolveAccount(id)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ck, err := s.accountCookies(acc)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	var shelfResult *weread.Shelf
	err = s.authedCall(ctx, acc, ck, func() error {
		shelf, fetchErr := s.wc.FetchShelf(ctx, ck)
		if fetchErr != nil {
			return fetchErr
		}
		shelfResult = shelf
		return nil
	})
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, "获取书架失败："+err.Error())
		return
	}
	shelf := shelfResult

	// Keep a bounded page of archive details, and expose the cursor rather
	// than silently dropping everything after the first fifty books.
	seen := make(map[string]bool, len(shelf.Books))
	for _, b := range shelf.Books {
		seen[b.ID] = true
	}
	archiveIDs := make([]string, 0, len(shelf.ArchiveBookIDs))
	for _, id := range shelf.ArchiveBookIDs {
		if !seen[id] {
			seen[id] = true
			archiveIDs = append(archiveIDs, id)
		}
	}
	if archiveOffset > len(archiveIDs) {
		s.writeErr(w, http.StatusBadRequest, "归档列表已变化，请重新拉取书架")
		return
	}
	mainIDs := make([]string, 0, len(shelf.Books))
	for _, b := range shelf.Books {
		mainIDs = append(mainIDs, b.ID)
	}
	revisionData, _ := json.Marshal([][]string{mainIDs, archiveIDs})
	revision := fmt.Sprintf("%x", sha256.Sum256(revisionData))
	if archiveOffset > 0 && r.URL.Query().Get("archive_revision") != revision {
		s.writeErr(w, http.StatusConflict, "书架已变化，请重新拉取书架")
		return
	}
	end := archiveOffset + 50
	if end > len(archiveIDs) {
		end = len(archiveIDs)
	}
	pending := archiveIDs[archiveOffset:end]
	results := make([]*weread.Book, len(pending))
	archiveFailures := map[string]string{}
	if len(pending) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 3)
		for i, bid := range pending {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, bid string) {
				defer wg.Done()
				defer func() { <-sem }()
				b, err := s.wc.BookInfo(ctx, ck, bid)
				if err != nil || b == nil {
					message := "没有返回书籍详情"
					if err != nil {
						message = err.Error()
					}
					mu.Lock()
					archiveFailures[bid] = message
					mu.Unlock()
					return
				}
				results[i] = b
			}(i, bid)
		}
		wg.Wait()
	}

	books := []weread.Book{}
	if archiveOffset == 0 {
		books = append(books, shelf.Books...)
	}
	archived := 0
	for _, b := range results {
		if b != nil {
			books = append(books, *b)
			archived++
		}
	}
	nextArchiveOffset := -1
	if end < len(archiveIDs) {
		nextArchiveOffset = end
	}
	// 付费结构审计不再随书架请求同步给出：整架几百本书意味着几百次目录
	// 请求，会把这个接口拖到分钟级并触发 120s 超时。审计由前端翻到哪一页
	// 就按 POST /api/pool/audit 请求哪一页（结果带 10 分钟目录缓存）。

	// 「已在池中」按本账号视角标注（含共享）。
	items, err := s.st.PoolItemsForAccount(store.KindBook, acc.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取书池失败")
		return
	}
	inPool := make([]string, 0, len(items))
	for _, it := range items {
		if it.Value != "" {
			inPool = append(inPool, it.Value)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"books": books, "in_pool": inPool, "account_id": acc.ID,
		"declared": shelf.Declared, "archived": archived, "archive_groups": shelf.ArchiveCount,
		"archive_total": len(archiveIDs), "archive_processed": end,
		"archive_revision":    revision,
		"next_archive_offset": nextArchiveOffset, "archive_failures": archiveFailures,
	})
}

// handlePoolAudit 批量审计指定书籍的付费结构（账号视角）。书架列表接口为
// 了速度不再附带审计结果；前端展示到哪一页就只请求这一页的书籍（≤100），
// 重复翻页由目录缓存兜住。
func (s *Server) handlePoolAudit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID int64    `json:"account_id"`
		Books     []string `json:"books"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.Books) == 0 {
		s.writeErr(w, http.StatusBadRequest, "未指定任何书籍 ID")
		return
	}
	if len(body.Books) > 100 {
		s.writeErr(w, http.StatusBadRequest, "一次最多审计 100 本书")
		return
	}
	if body.AccountID < 0 {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	for _, v := range body.Books {
		if strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > 64 {
			s.writeErr(w, http.StatusBadRequest, "书籍 ID 无效")
			return
		}
	}
	acc, err := s.resolveAccount(body.AccountID)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ck, err := s.accountCookies(acc)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	// 预热第一本书的目录：若登录已失效，这里会先续期再继续，避免整批
	// 书的审计全部失败后才暴露「登录超时」。
	if len(body.Books) > 0 {
		_ = s.authedCall(ctx, acc, ck, func() error {
			_, probeErr := s.chapterInfos(ctx, acc.ID, ck, body.Books[0])
			return probeErr
		})
	}
	targets := make([]weread.Book, 0, len(body.Books))
	for _, id := range body.Books {
		targets = append(targets, weread.Book{ID: id})
	}
	audit, errs := s.auditBooks(ctx, acc.ID, ck, targets)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"audit": audit, "errors": errs, "account_id": acc.ID,
	})
}

// handlePoolShelfImport 把预览的书架条目写入书池（指定账号视角），可选按书
// 拉取目录章节。「未付费」（全书章章要钱且未购）的书跳过导入；部分付费的书
// 只写免费/已解锁章节，付费未购章节不入池。ignore_paid 用于已购全本/会员
// （无限卡）账号：目录无法区分「已购可读」与「未购」，由用户显式声明后
// 不再按付费标记过滤。
func (s *Server) handlePoolShelfImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID    int64                `json:"account_id"`
		WithChapters bool                 `json:"with_chapters"`
		ChaptersPer  int                  `json:"chapters_per_book"`
		IgnorePaid   bool                 `json:"ignore_paid"`
		Books        []shelfImportBook    `json:"books"`
		Audit        map[string]BookAudit `json:"audit"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.Books) == 0 {
		s.writeErr(w, http.StatusBadRequest, "未选择任何书籍")
		return
	}
	if len(body.Books) > 50 {
		s.writeErr(w, http.StatusBadRequest, "一次最多导入 50 本书籍")
		return
	}
	if body.AccountID < 0 {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	// per 语义：0 = 导入全部章节（无章数上限），>0 = 每本精确 per 章。
	per := body.ChaptersPer
	full := per == 0
	if per < 0 {
		per = 0
	}
	acc, err := s.resolveAccount(body.AccountID)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	scope := acc.ID
	var ck map[string]string
	if body.WithChapters {
		ck, err = s.accountCookies(acc)
		if err != nil {
			s.writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	var (
		mu           sync.Mutex
		chapterJobs  = make(chan shelfImportBook, len(body.Books))
		failures     = make(map[string]string, 0)
		booksAdded   = make([]string, 0)
		booksSkip    = make([]string, 0)
		chapAdded    = 0
		chapExisted  = 0
		chapConflict = 0
	)
	// 书籍逐个入池（本地 SQLite 单连接，串行足够快）。完全不可读（未付费）
	// 的书连书条目都不入池——章节与正文不可用，留着只会被轮换抽到。
	for _, b := range body.Books {
		if a, ok := body.Audit[b.BookID]; ok && a.Total > 0 && a.Priced == a.Total && !body.IgnorePaid {
			failures[b.BookID] = "未付费（全书章节都需购买或会员），已跳过导入"
			continue
		}
		if _, isNew, err := s.st.UpsertBookMeta(scope, b.BookID, truncateRunes(b.Title, 60), truncateRunes(b.Author, 40), truncateRunes(b.Cover, 300)); err == nil {
			if isNew {
				booksAdded = append(booksAdded, b.BookID)
			} else {
				booksSkip = append(booksSkip, b.BookID)
			}
		} else {
			failures[b.BookID] = "写入书池失败：" + err.Error()
			continue
		}
		if body.WithChapters {
			chapterJobs <- b
		}
	}
	close(chapterJobs)

	if body.WithChapters {
		// 预热第一本书的目录：登录已失效时先续期，避免整批书都在目录
		// 请求上撞「登录超时」。
		if len(body.Books) > 0 {
			_ = s.authedCall(ctx, acc, ck, func() error {
				_, probeErr := s.chapterInfos(ctx, scope, ck, body.Books[0].BookID)
				return probeErr
			})
		}
		// 低并发拉目录，失败再给一次单独重试的机会（瞬时风控/网络抖动
		// 常见于批量请求的第一轮）。
		workers := 2
		if len(body.Books) < workers {
			workers = len(body.Books)
		}
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for b := range chapterJobs {
					limit := per
					if full {
						limit = 0 // 0 = 不限量，导入全书可读章节
					}
					var added, existed, conflicts int
					var err error
					// TOC fetches are the most rate-limited part of the import.
					// Retry with backoff instead of giving up after one retry,
					// otherwise large shelf imports end with later books at 0
					// chapters while the books themselves are already in the pool.
					for attempt := 0; attempt < 3; attempt++ {
						added, existed, conflicts, err = s.fetchChaptersIntoPool(ctx, ck, scope, b, limit, body.IgnorePaid)
						if err == nil {
							break
						}
						if attempt < 2 {
							time.Sleep(time.Duration(attempt+1) * time.Second)
						}
					}
					if err != nil {
						s.log.Warn("导入书籍章节失败", "book", b.BookID, "title", b.Title, "err", err)
						mu.Lock()
						failures[b.BookID] = err.Error()
						mu.Unlock()
						continue
					}
					if added == 0 && existed == 0 {
						mu.Lock()
						if conflicts > 0 {
							failures[b.BookID] = fmt.Sprintf("没有导入任何章节：%d 个章节 ID 已归属其他书籍（上传书的目录会共用章节 ID，表现为全书 0 章）", conflicts)
						} else {
							failures[b.BookID] = "没有可读章节可导入（免费章节为空且未购任何付费章节）"
						}
						mu.Unlock()
						continue
					}
					mu.Lock()
					chapAdded += added
					chapExisted += existed
					chapConflict += conflicts
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	}

	tplGenerated, tplHint := s.maybeAutoGenerateTemplate(ctx, acc, booksAdded)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"books_added": booksAdded, "books_skipped": booksSkip,
		"chapters_added": chapAdded, "chapters_existed": chapExisted,
		"chapters_conflict": chapConflict, "failures": failures,
		"template_generated": tplGenerated, "template_hint": tplHint,
	})
}

// maybeAutoGenerateTemplate 导入书籍后，若该账号还没有请求模板，就自动走
// 一遍免抓包生成逻辑（阅读页取 psvts + UA 推导 appId）。成功与否不强绑
// 本次导入结果：失败只记录日志并在响应里带提示，用户可以手动重试或改用
// 抓包模板。含章节未导入（with_chapters=false）的导入同样适用——模板需要
// 的只是书池里有一本书。
func (s *Server) maybeAutoGenerateTemplate(ctx context.Context, acc *store.Account, booksAdded []string) (bool, string) {
	_, payload, err := s.st.AccountSecrets(acc.ID)
	if err != nil {
		return false, ""
	}
	if len(payload) > 0 {
		return false, "" // 已有专属模板，不动
	}
	genCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	pl, _, _, genErr := s.generateAccountTemplate(genCtx, acc, "")
	if genErr == nil {
		if saveErr := s.st.SavePayload(acc.ID, pl); saveErr != nil {
			genErr = saveErr
		}
	}
	if genErr != nil {
		s.log.Warn("导入后自动生成阅读模板失败", "account", acc.ID, "err", genErr)
		return false, genErr.Error()
	}
	s.log.Info("导入书籍后自动生成了阅读模板", "account", acc.ID)
	return true, ""
}

type shelfImportBook struct {
	BookID string `json:"book_id"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Cover  string `json:"cover,omitempty"`
}

// fetchChaptersIntoPool pulls one book's TOC and writes up to per chapter IDs
// into the chapter pool（账号视角，标题形如「书名 · 章节名」）。付费未购章节
// (官方目录 price=-1 或 price>0 且 paid=0) 默认不写入——它们读不开；目录无法
// 区分「已购/会员可读」，所以 ignorePaid=true 时由调用方显式声明不过滤。
// 返回（新增数，已在池数，ID 冲突数）；重复导入时"已在池"不为零，不算失
// 败。章节入库用单事务批量写：每章一条 autocommit 会把整本导入变成几千个
// 写事务。冲突（章节 ID 已归属其他书）单独计数，上传书（CB_）目录复用时
// 会大量出现，绝不能与「已在池」混记，否则用户看到导入成功却书籍 0 章。
func (s *Server) fetchChaptersIntoPool(ctx context.Context, ck map[string]string, accountID int64, b shelfImportBook, per int, ignorePaid bool) (int, int, int, error) {
	chs, err := s.chapterInfos(ctx, accountID, ck, b.BookID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("拉取目录失败：%w", err)
	}
	targets := make([]store.NewChapter, 0, len(chs))
	for _, ch := range chs {
		if per > 0 && len(targets) >= per {
			break // per<=0 表示不限制章数
		}
		// 「封面 / 版权信息 / 第一部」这类无正文的目录页（wordCount 1~4）不适合
		// 做心跳目标，导入时直接跳过。
		if ch.WordCount > 0 && ch.WordCount < 5 {
			continue
		}
		if ch.ID == "" || (!ignorePaid && ch.Unpaid()) {
			continue
		}
		targets = append(targets, store.NewChapter{
			Value:        ch.ID,
			Title:        truncateRunes(b.Title+" · "+ch.Title, 60),
			WordCount:    ch.WordCount,
			ChapterIndex: ch.Index,
		})
	}
	return s.st.AddChapters(accountID, b.BookID, targets)
}

// accountCookiesResolve 解析账号并取 Cookie。
func (s *Server) accountCookiesResolve(accountID int64) (map[string]string, error) {
	acc, err := s.resolveAccount(accountID)
	if err != nil {
		return nil, err
	}
	return s.accountCookies(acc)
}

// poolScope 从 query/body 提取账号视角，-1 表示调用方未指定（全部行）。
func poolScopeQuery(r *http.Request) int64 {
	if v, ok := parseInt64(r.URL.Query().Get("account_id")); ok {
		return v
	}
	return 0 // 未指定：由 resolvePoolView 回落到第一个账号
}

// resolvePoolView 把视角解析为目标账号。共享池已移除，所有书池条目都归属
// 某个账号；view<=0 时回落到第一个已绑定的账号。
func (s *Server) resolvePoolView(w http.ResponseWriter, view int64) (int64, bool) {
	if view > 0 {
		acc, err := s.st.GetAccount(view)
		if err != nil || acc == nil {
			s.writeErr(w, http.StatusBadRequest, "账号不存在")
			return 0, false
		}
		return view, true
	}
	acc, err := s.resolveAccount(0)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return 0, false
	}
	return acc.ID, true
}

func (s *Server) handlePoolGet(w http.ResponseWriter, r *http.Request) {
	view, ok := s.resolvePoolView(w, poolScopeQuery(r))
	if !ok {
		return
	}
	out := map[string]any{}
	// The pool overview only needs books and chapter counts. Returning every
	// chapter row here made a 8000-chapter pool serialize thousands of items on
	// each page load; the chapter drawer fetches its own paginated slice.
	books, err := s.st.PoolItemsForAccount(store.KindBook, view)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取书池失败")
		return
	}
	out["books"] = books
	chapterCounts, err := s.st.PoolChapterCountsForAccount(view)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "统计章节数失败")
		return
	}
	delete(chapterCounts, "") // 无归属章节不参与书籍卡片展示
	out["chapter_counts"] = chapterCounts
	out["book_total"] = len(books)
	// 账号视角附带「顺序书单」进度：书单下标 bi / 章节下标 ci。书单顺序与
	// books 返回顺序一致（专属在前、共享在后，各自按 sort_order），前端
	// 据此为每本书标注已读/在读进度。共享视角不展示（进度是账号的）。
	if view > 0 {
		if raw, _ := s.st.GetSetting(fmt.Sprintf("sequence_pos_%d", view)); raw != "" {
			var pos struct {
				Bi int `json:"bi"`
				Ci int `json:"ci"`
			}
			if err := json.Unmarshal([]byte(raw), &pos); err == nil && pos.Bi >= 0 && pos.Ci >= 0 {
				out["seq_pos"] = map[string]int{"bi": pos.Bi, "ci": pos.Ci}
			}
		}
	}
	out["account_id"] = view
	s.writeJSON(w, http.StatusOK, out)
}

// handlePoolBookChapters 返回某本书链接的章节池条目（账号视角）。refresh=1
// 时额外调用一次官方章节接口实时核对付费结构，返回 live:{total, priced}。
func (s *Server) handlePoolBookChapters(w http.ResponseWriter, r *http.Request) {
	bookValue := r.PathValue("value")
	if strings.TrimSpace(bookValue) == "" {
		s.writeErr(w, http.StatusBadRequest, "书籍 ID 不能为空")
		return
	}
	view, ok := s.resolvePoolView(w, poolScopeQuery(r))
	if !ok {
		return
	}
	limit, offset := 50, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}
	items, total, enabled, err := s.st.PoolChaptersPage(bookValue, view, limit, offset)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取章节失败")
		return
	}
	out := map[string]any{
		"chapters": items, "total": total, "enabled": enabled,
		"limit": limit, "offset": offset,
		"book_id": bookValue, "account_id": view,
	}

	if r.URL.Query().Get("refresh") == "1" {
		if acc, err := s.resolveAccount(view); err == nil && view > 0 {
			if ck, err := s.accountCookies(acc); err == nil {
				ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
				defer cancel()
				var liveChs []weread.Chapter
				callErr := s.authedCall(ctx, acc, ck, func() error {
					chs2, infoErr := s.chapterInfos(ctx, acc.ID, ck, bookValue)
					if infoErr != nil {
						return infoErr
					}
					liveChs = chs2
					return nil
				})
				if callErr == nil {
					chs := liveChs
					total := len(chs)
					priced := 0
					unpaid := make(map[string]bool, 16)
					for _, ch := range chs {
						if ch.Unpaid() {
							priced++
							unpaid[ch.ID] = true
						}
					}
					out["live"] = map[string]any{
						"total":  total,
						"priced": priced,
					}
					// 给章节列表逐条标注：true = 官方目录标付费未购；
					// 目录里已消失的条目不在 map 中（按未知处理）。
					out["unpaid"] = unpaid
				} else {
					out["live_error"] = callErr.Error()
				}
			}
		}
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handlePoolBookPrune 对照官方目录清理某本书在池里读不开的付费未购章节
// （官方已判付费未购、或已从目录消失的条目）。账号视角跟随 query account_id。
func (s *Server) handlePoolBookPrune(w http.ResponseWriter, r *http.Request) {
	bookValue := r.PathValue("value")
	if strings.TrimSpace(bookValue) == "" {
		s.writeErr(w, http.StatusBadRequest, "书籍 ID 不能为空")
		return
	}
	view := poolScopeQuery(r)
	acc, err := s.resolveAccount(view)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ck, err := s.accountCookies(acc)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var chs []weread.Chapter
	err = s.authedCall(ctx, acc, ck, func() error {
		chs2, infoErr := s.chapterInfos(ctx, acc.ID, ck, bookValue)
		if infoErr != nil {
			return infoErr
		}
		chs = chs2
		return nil
	})
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, "拉取官方目录失败："+err.Error())
		return
	}
	unpaid := make(map[string]bool, len(chs))
	for _, ch := range chs {
		if ch.Unpaid() {
			unpaid[ch.ID] = true
		}
	}
	items, err := s.st.PoolChaptersOf(bookValue, view)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取章节失败")
		return
	}
	removed, kept := make([]string, 0), 0
	for _, it := range items {
		if unpaid[it.Value] {
			if err := s.st.DeletePoolItem(it.ID); err == nil {
				removed = append(removed, it.Value)
				continue
			}
		}
		kept++
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "removed": removed, "removed_count": len(removed), "kept": kept,
	})
}

func (s *Server) handlePoolAdd(w http.ResponseWriter, r *http.Request) {
	var body poolItemBody
	if !s.decode(w, r, &body) {
		return
	}
	if body.Kind != store.KindBook && body.Kind != store.KindChapter {
		s.writeErr(w, http.StatusBadRequest, "kind 只能是 book 或 chapter")
		return
	}
	if body.Value = strings.TrimSpace(body.Value); body.Value == "" {
		s.writeErr(w, http.StatusBadRequest, "ID 不能为空")
		return
	}
	if utf8.RuneCountInString(body.Value) > 64 {
		s.writeErr(w, http.StatusBadRequest, "ID 过长")
		return
	}
	scope, err := s.poolWriteScope(body.AccountID, body.Kind, body.BookID)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.addLinkedPoolItem(body.Kind, body.Value, truncateRunes(strings.TrimSpace(body.Title), 60), body.BookID, scope)
	if errors.Is(err, store.ErrPoolItemExists) {
		s.writeErr(w, http.StatusConflict, kindLabel(body.Kind)+"中已存在该 ID")
		return
	}
	if errors.Is(err, store.ErrChapterConflict) {
		s.writeErr(w, http.StatusConflict, "该章节 ID 已归属其他书籍（上传书目录可能共用章节 ID）")
		return
	}
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "添加失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// poolItemBody matches the JSON shape of a single pool item in requests.
type poolItemBody struct {
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Title     string `json:"title"`
	AccountID int64  `json:"account_id"`
	BookID    string `json:"book_id"`
}

// Validate the local destination before writing anything. Reading credentials
// are unnecessary when managing an existing account's local pool.
func (s *Server) poolWriteScope(accountID int64, kind, bookID string) (int64, error) {
	acc, err := s.resolveAccount(accountID)
	if err != nil {
		return 0, err
	}
	if kind == store.KindChapter {
		bookID = strings.TrimSpace(bookID)
		if bookID == "" {
			return 0, errors.New("添加章节时必须指定所属书籍 ID")
		}
		books, err := s.st.PoolItemsForAccount(store.KindBook, acc.ID)
		if err != nil {
			return 0, err
		}
		found := false
		for _, book := range books {
			if book.Value == bookID {
				found = true
				break
			}
		}
		if !found {
			return 0, errors.New("所属书籍不在该账号的书池中")
		}
	}
	return acc.ID, nil
}

func (s *Server) addLinkedPoolItem(kind, value, title, bookID string, accountID int64) (int64, error) {
	if kind == store.KindChapter {
		return s.st.AddChapterItem(accountID, bookID, value, title, 0)
	}
	return s.st.AddPoolItem(kind, value, title, accountID)
}

// handlePoolImport 批量导入：把一段文本中的所有 ID（空白/逗号分隔）全部
// 入池（指定账号视角），已存在的自动跳过，返回新增与跳过的数量。
func (s *Server) handlePoolImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind      string `json:"kind"`
		Text      string `json:"text"`
		AccountID int64  `json:"account_id"`
		BookID    string `json:"book_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Kind != store.KindBook && body.Kind != store.KindChapter {
		s.writeErr(w, http.StatusBadRequest, "kind 只能是 book 或 chapter")
		return
	}
	values := strings.FieldsFunc(body.Text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == ';' || r == '"' || r == '\''
	})
	if len(values) == 0 {
		s.writeErr(w, http.StatusBadRequest, "没有解析到任何 ID")
		return
	}
	if len(values) > 200 {
		s.writeErr(w, http.StatusBadRequest, "一次最多导入 200 个 ID")
		return
	}
	scope, err := s.poolWriteScope(body.AccountID, body.Kind, body.BookID)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	added, skipped := make([]string, 0), make([]string, 0)
	failures := make(map[string]string)
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || utf8.RuneCountInString(v) > 64 {
			continue
		}
		if _, err := s.addLinkedPoolItem(body.Kind, v, "", body.BookID, scope); err == nil {
			added = append(added, v)
		} else if errors.Is(err, store.ErrPoolItemExists) {
			skipped = append(skipped, v)
		} else {
			failures[v] = err.Error()
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": len(failures) == 0, "added": added, "skipped": skipped, "failures": failures,
	})
}

func (s *Server) handlePoolUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "条目 ID 无效")
		return
	}
	var body struct {
		Value   *string `json:"value"`
		Title   *string `json:"title"`
		Enabled *bool   `json:"enabled"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Value != nil {
		v := strings.TrimSpace(*body.Value)
		if v == "" {
			s.writeErr(w, http.StatusBadRequest, "ID 不能为空")
			return
		}
		if utf8.RuneCountInString(v) > 64 {
			s.writeErr(w, http.StatusBadRequest, "ID 过长")
			return
		}
		*body.Value = v
	}
	if body.Title != nil {
		*body.Title = truncateRunes(strings.TrimSpace(*body.Title), 60)
	}
	if err := s.st.UpdatePoolItem(id, body.Value, body.Title, body.Enabled); err != nil {
		if errors.Is(err, store.ErrPoolItemExists) {
			s.writeErr(w, http.StatusConflict, "该 ID 已在池中")
			return
		}
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "不存在") || strings.Contains(err.Error(), "不能为空") {
			status = http.StatusBadRequest
		}
		s.writeErr(w, status, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePoolDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "条目 ID 无效")
		return
	}
	if err := s.st.DeletePoolItem(id); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "不存在") {
			status = http.StatusNotFound
		}
		s.writeErr(w, status, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePoolMove 上移/下移一本书（顺序书单的阅读顺序）。
func (s *Server) handlePoolMove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "条目 ID 无效")
		return
	}
	var body struct {
		Direction int `json:"direction"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Direction != 1 && body.Direction != -1 {
		s.writeErr(w, http.StatusBadRequest, "direction 只能为 1（下移）或 -1（上移）")
		return
	}
	if err := s.st.MovePoolItem(id, body.Direction); err != nil {
		if strings.Contains(err.Error(), "不存在") {
			s.writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeErr(w, http.StatusInternalServerError, "调整顺序失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePoolReset 清空某视角某类池；引擎随即将回落到内置兜底。
func (s *Server) handlePoolReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind      string `json:"kind"`
		AccountID int64  `json:"account_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Kind != store.KindBook && body.Kind != store.KindChapter {
		s.writeErr(w, http.StatusBadRequest, "kind 只能是 book 或 chapter")
		return
	}
	if body.AccountID < 0 {
		body.AccountID = 0
	}
	if err := s.st.ClearPoolFor(body.Kind, body.AccountID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "清空失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 账号指纹模板 ----------

// 全局默认模板属旧契约：接口与存储读取已随全局模板 UI 一并删除
// （缺陷 13），模板唯一来源是各账号保存的专属模板。

func kindLabel(kind string) string {
	if kind == store.KindChapter {
		return "章节池"
	}
	return "书池"
}

// chapterInfoTTL keeps repeated shelf audits, imports and chapter-drawer
// refreshes from hitting the rate-limited TOC endpoint again. The cache is
// scoped by account because paid/unpaid flags are account-specific.
const chapterInfoTTL = 10 * time.Minute

// chapterCacheMax bounds the in-memory TOC cache. Entries are complete chapter
// lists (with per-chapter titles); without a cap a big multi-account shelf
// could keep tens of MB of live payloads until restart.
const chapterCacheMax = 200

// putChapterCache stores fetched chs under key, dropping expired entries first
// and then the soonest-to-expire entry when the cap is still exceeded.
func (s *Server) putChapterCache(key string, chs []weread.Chapter) {
	now := time.Now()
	if len(s.chapterCache) >= chapterCacheMax {
		for k, v := range s.chapterCache {
			if now.After(v.expires) {
				delete(s.chapterCache, k)
			}
		}
	}
	if len(s.chapterCache) >= chapterCacheMax {
		oldestKey := ""
		var oldest time.Time
		first := true
		for k, v := range s.chapterCache {
			if first || v.expires.Before(oldest) {
				oldestKey, oldest, first = k, v.expires, false
			}
		}
		delete(s.chapterCache, oldestKey)
	}
	s.chapterCache[key] = chapterCacheEntry{chapters: chs, expires: now.Add(chapterInfoTTL)}
}

func (s *Server) chapterInfos(ctx context.Context, accountID int64, ck map[string]string, bookID string) ([]weread.Chapter, error) {
	key := fmt.Sprintf("%d:%s", accountID, bookID)
	if accountID > 0 {
		s.chapterMu.Lock()
		if s.chapterCache == nil {
			s.chapterCache = map[string]chapterCacheEntry{}
		}
		entry, ok := s.chapterCache[key]
		s.chapterMu.Unlock()
		if ok && time.Now().Before(entry.expires) {
			return entry.chapters, nil
		}
	}
	chs, err := s.wc.ChapterInfos(ctx, ck, bookID)
	if err != nil {
		return nil, err
	}
	if accountID > 0 {
		s.chapterMu.Lock()
		if s.chapterCache == nil {
			s.chapterCache = map[string]chapterCacheEntry{}
		}
		s.putChapterCache(key, chs)
		s.chapterMu.Unlock()
	}
	return chs, nil
}
