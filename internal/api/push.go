package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"wxread-panel/internal/store"
)

var pushTypes = map[string]string{
	"pushplus":   "PushPlus",
	"wxpusher":   "WxPusher",
	"telegram":   "Telegram",
	"serverchan": "ServerChan",
}

func validPushType(t string) bool {
	_, ok := pushTypes[strings.ToLower(t)]
	return ok
}

func validatePushConfig(t string, cfg map[string]string) string {
	switch strings.ToLower(t) {
	case "pushplus":
		if strings.TrimSpace(cfg["token"]) == "" {
			return "请填写 PushPlus token"
		}
	case "wxpusher":
		if strings.TrimSpace(cfg["spt"]) == "" {
			return "请填写 WxPusher SPT"
		}
	case "telegram":
		if strings.TrimSpace(cfg["bot_token"]) == "" || strings.TrimSpace(cfg["chat_id"]) == "" {
			return "请填写 Telegram Bot Token 和 Chat ID"
		}
	case "serverchan":
		if strings.TrimSpace(cfg["sendkey"]) == "" {
			return "请填写 ServerChan SendKey"
		}
	default:
		return "不支持的推送类型"
	}
	return ""
}

func (s *Server) handlePushList(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListPushChannels()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取推送渠道失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channels": list})
}

func (s *Server) handlePushCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string            `json:"name"`
		Type    string            `json:"type"`
		Config  map[string]string `json:"config"`
		Enabled *bool             `json:"enabled"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	typ := strings.ToLower(strings.TrimSpace(body.Type))
	if !validPushType(typ) {
		s.writeErr(w, http.StatusBadRequest, "不支持的推送类型")
		return
	}
	if msg := validatePushConfig(typ, body.Config); msg != "" {
		s.writeErr(w, http.StatusBadRequest, msg)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = pushTypes[typ]
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	v := store.PushChannel{Name: name, Type: typ, Config: body.Config, Enabled: enabled}
	if err := s.st.CreatePushChannel(&v); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "创建推送渠道失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channel": v})
}

func (s *Server) handlePushUpdate(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.GetPushChannel(pathID(r))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取推送渠道失败")
		return
	}
	if v == nil {
		s.writeErr(w, http.StatusNotFound, "推送渠道不存在")
		return
	}
	var body struct {
		Name    *string           `json:"name"`
		Type    *string           `json:"type"`
		Config  map[string]string `json:"config"`
		Enabled *bool             `json:"enabled"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Type != nil {
		typ := strings.ToLower(strings.TrimSpace(*body.Type))
		if !validPushType(typ) {
			s.writeErr(w, http.StatusBadRequest, "不支持的推送类型")
			return
		}
		v.Type = typ
	}
	if body.Config != nil {
		v.Config = body.Config
	}
	if msg := validatePushConfig(v.Type, v.Config); msg != "" {
		s.writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		v.Name = strings.TrimSpace(*body.Name)
	}
	if body.Enabled != nil {
		v.Enabled = *body.Enabled
	}
	if err := s.st.UpdatePushChannel(v); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存推送渠道失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"channel": v})
}

func (s *Server) handlePushDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeletePushChannel(pathID(r)); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "删除推送渠道失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.GetPushChannel(pathID(r))
	if err != nil || v == nil {
		s.writeErr(w, http.StatusNotFound, "推送渠道不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	content := "如果你收到这条消息，说明「读时」的推送配置已经生效。\n发送时间：" +
		time.Now().In(s.timezone()).Format("2006-01-02 15:04:05")
	if err := s.nt.Send(ctx, *v, "读时 · 测试推送", content); err != nil {
		s.writeErr(w, http.StatusBadGateway, "推送失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
