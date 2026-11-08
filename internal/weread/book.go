package weread

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Book is the subset of book metadata the panel needs.
type Book struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Author   string    `json:"author"`
	Cover    string    `json:"cover"`
	Intro    string    `json:"intro"`
	Chapters []Chapter `json:"chapters,omitempty"`
}

// Chapter is one entry of a book's table of contents. Paid/Price reflect the
// current account's view: free chapters carry Price=0; locked ones carry
// Price<0 (official web protocol convention, e.g. -1) and historical layouts
// may use Price>0 with Paid=0. Purchased chapters report Paid=1.
type Chapter struct {
	ID        string  `json:"id"`
	UID       int64   `json:"uid"`
	Index     int     `json:"index"`
	Title     string  `json:"title"`
	WordCount int     `json:"word_count"`
	Paid      int     `json:"paid"`
	Price     float64 `json:"price"`
}

// Unpaid reports whether this chapter costs money and the current account has
// not unlocked it yet (heartbeats and the reader cannot open such chapters, so
// they must never enter the reading pool).
func (ch Chapter) Unpaid() bool {
	return ch.Paid == 0 && ch.Price != 0
}

// RawJSON sends one raw request and decodes the response into any value; used
// by diagnostics tools to inspect protocol responses without shaping.
func (c *Client) RawJSON(ctx context.Context, method, path string, ck Cookies, body, out any) (*http.Response, error) {
	return c.doJSON(ctx, method, path, ck, body, out)
}

// SearchBooks queries the global search endpoint and returns book hits.
// The answer is { books: [{ bookInfo: { bookId, title, author, cover, intro },
// ... }] }; numeric bookIds may be as short as 6 chars, so NO length filter is
// applied (that used to drop virtually every mainstream paperback).
func (c *Client) SearchBooks(ctx context.Context, ck Cookies, keyword string) ([]Book, error) {
	q := url.Values{}
	q.Set("keyword", keyword)
	q.Set("maxIdx", "0")
	q.Set("fragmentSize", "120")
	q.Set("count", "15")

	var raw any
	if _, err := c.doJSON(ctx, http.MethodGet, "/web/search/global?"+q.Encode(), ck, nil, &raw); err != nil {
		return nil, err
	}

	books := make([]Book, 0, 15)
	seen := map[string]bool{}
	pickBook := func(x map[string]any) {
		id := pickString(x, "bookId", "book_id", "id")
		title := pickString(x, "title", "bookName", "name")
		if id == "" || title == "" || (id == "mpbook" || strings.HasPrefix(id, "MP_WXS_") || seen[id]) {
			return
		}
		seen[id] = true
		books = append(books, Book{
			ID:     id,
			Title:  title,
			Author: pickString(x, "author", "authorName", "author_name"),
			Cover:  pickString(x, "cover", "coverUrl", "cover_url"),
			Intro:  pickString(x, "intro", "description", "abstract"),
		})
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if nested, ok := x["bookInfo"].(map[string]any); ok {
				pickBook(nested)
			} else {
				pickBook(x)
			}
			for _, val := range x {
				switch val.(type) {
				case map[string]any, []any:
					walk(val)
				}
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(raw)
	return books, nil
}

// Shelf is the result of one full shelf sync.
type Shelf struct {
	Books          []Book   // 主书架全部书籍
	Declared       int      // 服务端声明的 bookCount，与 Books 长度一致说明无遗漏
	ArchiveCount   int      // 书架分组（归档）数量
	ArchiveBookIDs []string // 分组内的书籍 ID（需要逐本查询详情）
}

// FetchShelf pulls the user's own bookshelf from the web protocol
// (GET /web/shelf/sync?userVid=&synckey=0). The two query params are required:
// userVid must be an EMPTY string (any value triggers -2012) and synckey=0
// forces a full list. The response also carries the archive(te 分组)列表，
// whose books are NOT part of the main books array—— callers should query
// AT通过 BookInfo 逐本补齐 ArchiveBookIDs。
func (c *Client) FetchShelf(ctx context.Context, ck Cookies) (*Shelf, error) {
	var raw any
	if _, err := c.doJSON(ctx, http.MethodGet, "/web/shelf/sync?userVid=&synckey=0", ck, nil, &raw); err != nil {
		return nil, err
	}
	if m, ok := raw.(map[string]any); ok {
		if code := toInt(m["errCode"]); code != 0 {
			return nil, fmt.Errorf("%s（errCode=%d）", apiErrText(m, code), code)
		}
	}

	shelf := &Shelf{Books: []Book{}}
	seen := map[string]bool{}
	addBook := func(id, title, author, cover string) {
		if id == "" || title == "" || seen[id] {
			return
		}
		// MP_WXS_* 是公众号而非书籍；"mpbook" 是官方的「文章收藏」虚拟条目
		// （收藏的公众号文章合集）。三者都无法用书籍接口阅读，直接排除。
		// 注意书架允许任意长度的 bookId（包括 6~8 位的纯数字型），不要加
		// 长度过滤——那会把大量正常的数字书误杀。
		if strings.HasPrefix(id, "MP_WXS_") || id == "mpbook" {
			return
		}
		seen[id] = true
		shelf.Books = append(shelf.Books, Book{ID: id, Title: title, Author: author, Cover: cover})
	}

	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			id := pickString(x, "bookId", "book_id")
			title := pickString(x, "title", "bookName", "name")
			addBook(id, title,
				pickString(x, "author", "authorName", "author_name"),
				pickString(x, "cover", "coverUrl", "cover_url"))
			for _, val := range x {
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(raw)

	if m, ok := raw.(map[string]any); ok {
		shelf.Declared = int(toInt(m["bookCount"]))
		if arr, ok := m["archive"].([]any); ok {
			for _, item := range arr {
				if group, ok := item.(map[string]any); ok {
					shelf.ArchiveCount++
					if ids, ok := group["bookIds"].([]any); ok {
						for _, idv := range ids {
							if id, ok := idv.(string); ok && !strings.HasPrefix(id, "MP_WXS_") {
								shelf.ArchiveBookIDs = append(shelf.ArchiveBookIDs, id)
							}
						}
					}
				}
			}
		}
	}
	return shelf, nil
}

// ChapterInfos returns the table of contents for a book.
func (c *Client) ChapterInfos(ctx context.Context, ck Cookies, bookID string) ([]Chapter, error) {
	body := map[string]any{"bookIds": []string{bookID}}
	var raw any
	if _, err := c.doJSON(ctx, http.MethodPost, "/web/book/chapterInfos", ck, body, &raw); err != nil {
		return nil, err
	}
	// 接口在登录态失效/风控等情况下返回 errCode + 空目录；如果不检查，
	// walk 会把空结果当成“该书没有目录”，掩盖真实报错。
	if m, ok := raw.(map[string]any); ok {
		if code := toInt(m["errCode"]); code != 0 {
			return nil, fmt.Errorf("%s（errCode=%d）", apiErrText(m, code), code)
		}
	}

	chapters := make([]Chapter, 0, 64)
	seen := map[int64]bool{}

	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			bID := pickString(x, "bookId", "book_id")
			for _, key := range []string{"updated", "chapterInfos", "chapters", "chapterList"} {
				arr, ok := x[key].([]any)
				if !ok {
					continue
				}
				if bID != "" && bID != bookID {
					continue
				}
				for _, item := range arr {
					m, ok := item.(map[string]any)
					if !ok {
						continue
					}
					uid := toInt64(m["chapterUid"])
					if uid == 0 {
						uid = toInt64(m["chapterId"])
					}
					if uid != 0 && seen[uid] {
						continue
					}
					ch := Chapter{
						ID:        pickString(m, "chapterId", "cid", "chapter_id"),
						UID:       uid,
						Index:     toInt(m["chapterIdx"]),
						Title:     pickString(m, "title", "chapterTitle", "chapterName"),
						WordCount: toInt(m["wordCount"]),
						Paid:      toInt(m["paid"]),
					}
					if v, ok := m["price"].(float64); ok {
						ch.Price = v
					}
					if ch.UID == 0 && ch.Index == 0 && ch.Title == "" {
						continue
					}
					if ch.UID != 0 {
						seen[ch.UID] = true
					}
					// 接口大多只返回数字 chapterUid，不返回编码后的 chapterId；
					// 心跳与阅读器使用的 c 是编码值，这里就地补齐。
					if ch.ID == "" && ch.UID != 0 {
						ch.ID = ChapterIDFromUID(ch.UID)
					}
					chapters = append(chapters, ch)
				}
			}
			for k, val := range x {
				if k == "updated" || k == "chapterInfos" || k == "chapters" || k == "chapterList" {
					continue
				}
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(raw)

	sort.SliceStable(chapters, func(i, j int) bool {
		if chapters[i].Index == chapters[j].Index {
			return chapters[i].UID < chapters[j].UID
		}
		return chapters[i].Index < chapters[j].Index
	})
	return chapters, nil
}

// BookInfo fetches metadata for one book.
func (c *Client) BookInfo(ctx context.Context, ck Cookies, bookID string) (*Book, error) {
	if bookID == "" {
		return nil, fmt.Errorf("bookId 不能为空")
	}
	var raw any
	if _, err := c.doJSON(ctx, http.MethodGet, "/web/book/info?bookId="+url.QueryEscape(bookID), ck, nil, &raw); err != nil {
		return nil, err
	}

	book := &Book{ID: bookID}
	var find func(any) bool
	find = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			if pickString(x, "bookId", "book_id") == bookID {
				book.Title = pickString(x, "title", "bookName", "name")
				book.Author = pickString(x, "author", "authorName", "author_name")
				book.Cover = pickString(x, "cover", "coverUrl", "cover_url")
				book.Intro = pickString(x, "intro", "description", "abstract")
				return book.Title != ""
			}
			for _, val := range x {
				if find(val) {
					return true
				}
			}
		case []any:
			for _, val := range x {
				if find(val) {
					return true
				}
			}
		}
		return false
	}
	if !find(raw) {
		return nil, fmt.Errorf("未找到书籍信息")
	}
	return book, nil
}

// DecodePayload converts a captured JSON body into a template map, removing
// volatile signature fields.
func DecodePayload(s string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("请求体为空")
	}
	delete(m, "s")
	delete(m, "sg")
	return m, nil
}
