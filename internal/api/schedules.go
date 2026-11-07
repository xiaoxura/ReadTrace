package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"readtrace/internal/store"
)

func normalizeCron(expr string) string {
	return strings.Join(strings.Fields(expr), " ")
}

func (s *Server) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListSchedules()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取定时任务失败")
		return
	}
	next := s.sch.NextTimes()
	for i := range list {
		if n, ok := next[list[i].ID]; ok && list[i].Enabled {
			list[i].NextRunAt = n
		}
		if !list[i].Enabled {
			list[i].NextRunAt = 0
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].AccountID == list[j].AccountID {
			return list[i].ID < list[j].ID
		}
		return list[i].AccountID < list[j].AccountID
	})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"schedules": list,
		"timezone":  s.sch.Timezone().String(),
	})
}

func (s *Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID int64  `json:"account_id"`
		Cron      string `json:"cron"`
		Minutes   int    `json:"minutes"`
		Enabled   *bool  `json:"enabled"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	acc, ok := s.accountOrErr(w, body.AccountID)
	if !ok {
		return
	}
	cronExpr := normalizeCron(body.Cron)
	if err := s.sch.Validate(cronExpr); err != nil {
		s.writeErr(w, http.StatusBadRequest, "Cron 表达式无效："+err.Error())
		return
	}
	if body.Minutes <= 0 {
		body.Minutes = acc.DefaultMinutes
	}
	if body.Minutes <= 0 || body.Minutes > 24*60 {
		s.writeErr(w, http.StatusBadRequest, "阅读时长需在 1~1440 分钟之间")
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	v := store.Schedule{
		AccountID: acc.ID, Cron: cronExpr, Minutes: body.Minutes, Enabled: enabled,
	}
	if err := s.st.CreateSchedule(&v); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "创建定时任务失败")
		return
	}
	if enabled {
		if err := s.sch.Add(&v); err != nil {
			_ = s.st.DeleteSchedule(v.ID)
			s.writeErr(w, http.StatusBadRequest, "注册定时任务失败："+err.Error())
			return
		}
		v.NextRunAt = s.sch.Next(v.ID)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"schedule": v})
}

func (s *Server) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	v, err := s.st.GetSchedule(id)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取定时任务失败")
		return
	}
	if v == nil {
		s.writeErr(w, http.StatusNotFound, "定时任务不存在")
		return
	}
	var body struct {
		Cron    *string `json:"cron"`
		Minutes *int    `json:"minutes"`
		Enabled *bool   `json:"enabled"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Cron != nil {
		cronExpr := normalizeCron(*body.Cron)
		if err := s.sch.Validate(cronExpr); err != nil {
			s.writeErr(w, http.StatusBadRequest, "Cron 表达式无效："+err.Error())
			return
		}
		v.Cron = cronExpr
	}
	if body.Minutes != nil {
		if *body.Minutes <= 0 || *body.Minutes > 24*60 {
			s.writeErr(w, http.StatusBadRequest, "阅读时长需在 1~1440 分钟之间")
			return
		}
		v.Minutes = *body.Minutes
	}
	if body.Enabled != nil {
		v.Enabled = *body.Enabled
	}
	if err := s.st.UpdateSchedule(v); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存定时任务失败")
		return
	}
	if v.Enabled {
		if err := s.sch.Add(v); err != nil {
			s.writeErr(w, http.StatusBadRequest, "注册定时任务失败："+err.Error())
			return
		}
		v.NextRunAt = s.sch.Next(v.ID)
	} else {
		s.sch.Remove(v.ID)
		v.NextRunAt = 0
		_ = s.st.SetScheduleTimes(v.ID, v.LastRunAt, 0)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"schedule": v})
}

func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	s.sch.Remove(id)
	if err := s.st.DeleteSchedule(id); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "删除定时任务失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	expr := normalizeCron(r.URL.Query().Get("cron"))
	if expr == "" {
		s.writeErr(w, http.StatusBadRequest, "缺少 cron 表达式")
		return
	}
	n, _ := parseInt64(r.URL.Query().Get("n"))
	times, err := s.sch.Preview(expr, int(n))
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, "Cron 表达式无效："+err.Error())
		return
	}
	loc := s.sch.Timezone()
	weekdays := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	out := make([]map[string]any, 0, len(times))
	for _, t := range times {
		local := t.In(loc)
		out = append(out, map[string]any{
			"unix": local.Unix(),
			"text": local.Format("2006-01-02 15:04") + " " + weekdays[int(local.Weekday())],
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"times": out, "timezone": loc.String(), "now": time.Now().In(loc).Format(time.RFC3339),
	})
}
