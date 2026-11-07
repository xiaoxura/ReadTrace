package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"readtrace/internal/store"
)

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID int64 `json:"account_id"`
		Minutes   int   `json:"minutes"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	acc, ok := s.accountOrErr(w, body.AccountID)
	if !ok {
		return
	}
	run, err := s.eng.Start(acc, body.Minutes, "manual")
	if err != nil {
		s.writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"run": run, "ok": true})
}

func (s *Server) handleRunStop(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := s.eng.Stop(id); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRunList(w http.ResponseWriter, r *http.Request) {
	accountID, _ := strconv.ParseInt(r.URL.Query().Get("account_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.st.ListRuns(accountID, limit)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取运行记录失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "active": s.eng.Snapshot()})
}

func (s *Server) handleRunGet(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.GetRun(pathID(r))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取运行记录失败")
		return
	}
	if run == nil {
		s.writeErr(w, http.StatusNotFound, "运行记录不存在")
		return
	}
	logs, _ := s.st.ListLogs(run.ID, 0, 300)
	s.writeJSON(w, http.StatusOK, map[string]any{"run": run, "logs": logs})
}

func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var logs []store.RunLog
	var err error
	if r.URL.Query().Get("tail") == "1" {
		logs, err = s.st.RecentLogs(id, limit)
	} else {
		logs, err = s.st.ListLogs(id, afterID, limit)
	}
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取日志失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeErr(w, http.StatusInternalServerError, "当前环境不支持流式输出")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	subID, ch := s.eng.Hub().Subscribe()
	defer s.eng.Hub().Unsubscribe(subID)

	writeEvent := func(event string, data any) bool {
		payload, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeEvent("hello", map[string]any{
		"active": s.eng.Snapshot(),
		"now":    time.Now().Unix(),
		"server": s.version,
	}) {
		return
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case ev, open := <-ch:
			if !open {
				return
			}
			if !writeEvent("message", ev) {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
