package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"wxread-panel/internal/store"
)

func (s *Server) resolveAccount(id int64) (*store.Account, error) {
	if id > 0 {
		acc, err := s.st.GetAccount(id)
		if err != nil {
			return nil, err
		}
		if acc == nil {
			return nil, errors.New("账号不存在")
		}
		return acc, nil
	}
	list, err := s.st.ListAccounts()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errors.New("请先添加微信读书账号")
	}
	return &list[0], nil
}

func (s *Server) accountCookies(acc *store.Account) (map[string]string, error) {
	ck, _, err := s.st.AccountSecrets(acc.ID)
	if err != nil {
		return nil, err
	}
	if len(ck) == 0 {
		return nil, errors.New("账号没有可用的登录凭据")
	}
	return ck, nil
}

func (s *Server) handleBookSearch(w http.ResponseWriter, r *http.Request) {
	id, _ := parseInt64(r.URL.Query().Get("account_id"))
	acc, err := s.resolveAccount(id)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		s.writeErr(w, http.StatusBadRequest, "请输入搜索关键词")
		return
	}
	ck, err := s.accountCookies(acc)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	books, err := s.wc.SearchBooks(ctx, ck, q)
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, "搜索失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"books": books, "account_id": acc.ID})
}

func (s *Server) handleBookDetail(w http.ResponseWriter, r *http.Request) {
	bookID := r.PathValue("id")
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

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	book, err := s.wc.BookInfo(ctx, ck, bookID)
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, "获取书籍信息失败："+err.Error())
		return
	}
	chapters, chErr := s.wc.ChapterInfos(ctx, ck, bookID)
	warning := ""
	if chErr != nil {
		warning = "获取目录失败：" + chErr.Error()
	}
	hasChapterIDs := false
	for _, ch := range chapters {
		if ch.ID != "" {
			hasChapterIDs = true
			break
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"book": book, "chapters": chapters, "has_chapter_ids": hasChapterIDs,
		"warning": warning, "account_id": acc.ID,
	})
}
