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

// Chapter is one entry of a book's table of contents.
type Chapter struct {
	ID        string `json:"id"`
	UID       int64  `json:"uid"`
	Index     int    `json:"index"`
	Title     string `json:"title"`
	WordCount int    `json:"word_count"`
}

// SearchBooks queries the global search endpoint and returns book hits.
func (c *Client) SearchBooks(ctx context.Context, ck Cookies, keyword string) ([]Book, error) {
	q := url.Values{}
	q.Set("keyword", keyword)
	q.Set("maxIdx", "0")
	q.Set("fragmentSize", "120")
	q.Set("count", "10")

	var raw any
	if _, err := c.doJSON(ctx, http.MethodGet, "/web/search/global?"+q.Encode(), ck, nil, &raw); err != nil {
		return nil, err
	}

	books := make([]Book, 0, 10)
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if nested, ok := x["bookInfo"].(map[string]any); ok {
				x = nested
			}
			id := pickString(x, "bookId", "book_id")
			title := pickString(x, "title", "bookName", "name")
			if id != "" && title != "" && len(id) >= 10 && !seen[id] {
				seen[id] = true
				books = append(books, Book{
					ID:     id,
					Title:  title,
					Author: pickString(x, "author", "authorName", "author_name"),
					Cover:  pickString(x, "cover", "coverUrl", "cover_url"),
					Intro:  pickString(x, "intro", "description", "abstract"),
				})
			}
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

	if len(books) == 0 {
		return books, nil
	}
	return books, nil
}

// ChapterInfos returns the table of contents for a book.
func (c *Client) ChapterInfos(ctx context.Context, ck Cookies, bookID string) ([]Chapter, error) {
	body := map[string]any{"bookIds": []string{bookID}}
	var raw any
	if _, err := c.doJSON(ctx, http.MethodPost, "/web/book/chapterInfos", ck, body, &raw); err != nil {
		return nil, err
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
					}
					if ch.UID == 0 && ch.Index == 0 && ch.Title == "" {
						continue
					}
					if ch.UID != 0 {
						seen[ch.UID] = true
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
