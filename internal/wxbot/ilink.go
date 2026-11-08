// Package wxbot 接入微信官方机器人（腾讯 iLink Bot）渠道。
//
// iLink Bot 协议按公开接口行为独立实现：纯 HTTP + JSON，长轮询收消息，
// AES-128-ECB 加密的 CDN 媒体。本项目不包含任何第三方插件源代码。
package wxbot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultILinkBaseURL 是登录与 API 的默认域名；登录后服务端可能下发专属 baseurl。
	DefaultILinkBaseURL = "https://ilinkai.weixin.qq.com"
	// CDNBaseURL 用于媒体直传。
	CDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
	// botQRType 是官方插件使用的机器人类型（bot_type=3）。
	botQRType = "3"
	// channelVersion 标识本客户端的协议版本。
	channelVersion = "2.4.10"
	// botAgent 随 base_info 上报。
	botAgent = "ReadTrace"
	// pollTimeout 是 getUpdates 的客户端长轮询超时。
	pollTimeout = 35 * time.Second
	// callTimeout 覆盖普通 CGI 请求。
	callTimeout = 15 * time.Second
)

// ---- 协议模型（JSON 编解码；bytes 字段为 base64 字符串） ----

// u64str 容忍 uint64 以 JSON 数字或字符串两种形态出现，统一存为字符串，
// 避免 JS/浮点精度问题（服务端消息 ID 可能大于 2^53）。
type u64str string

func (s *u64str) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = u64str(v)
		return nil
	}
	*s = u64str(string(b))
	return nil
}

const (
	msgTypeUser   = 1
	msgTypeBot    = 2
	stateNew      = 0
	stateFinish   = 2
	itemTypeText  = 1
	itemTypeImage = 2
	mediaTypeImg  = 1
)

// MessageItem is one part of a message.
type MessageItem struct {
	Type        int               `json:"type,omitempty"`
	TextItem    *MessageTextItem  `json:"text_item,omitempty"`
	ImageItem   *MessageImageItem `json:"image_item,omitempty"`
	IsCompleted *bool             `json:"is_completed,omitempty"`
	// 入站的语音/文件/引用等结构暂不解析，忽略未知字段。
}

type MessageTextItem struct {
	Text string `json:"text,omitempty"`
}

// MessageImageItem mirrors CDNMedia + ImageItem for sending images.
type MessageImageItem struct {
	Media   ImageMedia `json:"media"`
	MidSize int64      `json:"mid_size,omitempty"`
}

type ImageMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"` // base64
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

// Message is one inbound/outbound message (proto WeixinMessage).
type Message struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    u64str        `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMS int64         `json:"create_time_ms,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
	RunID        string        `json:"run_id,omitempty"`
}

// Text returns the concatenated text of the message's text items.
func (m *Message) Text() string {
	var b bytes.Buffer
	for _, it := range m.ItemList {
		if it.Type == itemTypeText && it.TextItem != nil {
			b.WriteString(it.TextItem.Text)
		}
	}
	return b.String()
}

// Client is an authenticated iLink bot HTTP client.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a client for one bot credential.
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultILinkBaseURL
	}
	return &Client{baseURL: baseURL, token: token, http: &http.Client{Timeout: 0}}
}

// BaseURL returns the client's current base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// Token reports whether a token is configured.
func (c *Client) HasToken() bool { return c.token != "" }

func (c *Client) headers(auth bool) http.Header {
	h := http.Header{}
	h.Set("iLink-App-Id", "bot")
	h.Set("iLink-App-ClientVersion", channelVersion)
	if auth && c.token != "" {
		h.Set("Authorization", "Bearer "+c.token)
		h.Set("AuthorizationType", "ilink_bot_token")
	}
	return h
}

func (c *Client) do(ctx context.Context, method, rawPath string, hdr http.Header, body []byte) ([]byte, error) {
	base := c.baseURL
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	rel, err := url.Parse(rawPath)
	if err != nil {
		return nil, fmt.Errorf("非法路径 %q: %w", rawPath, err)
	}
	u := base + rel.Path
	if rel.RawQuery != "" {
		u += "?" + rel.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-WECHAT-UIN", randomWechatUin())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("iLink HTTP %d: %s", resp.StatusCode, clip(data, 200))
	}
	return data, nil
}

func (c *Client) postJSON(ctx context.Context, path string, body any, auth bool, timeout time.Duration) ([]byte, error) {
	if m, mapOK := body.(map[string]any); mapOK {
		m["base_info"] = map[string]any{"channel_version": channelVersion, "bot_agent": botAgent}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.do(cctx, http.MethodPost, path, c.headers(auth), raw)
}

func (c *Client) get(ctx context.Context, path string, auth bool, timeout time.Duration) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.do(cctx, http.MethodGet, path, c.headers(auth), nil)
}

func clip(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// randomWechatUin 按协议生成 X-WECHAT-UIN：随机 uint32 → 十进制字符串 → base64。
func randomWechatUin() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	n := (uint32(b[0]) << 24) | (uint32(b[1]) << 16) | (uint32(b[2]) << 8) | uint32(b[3])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

// ---- 机器人自身的微信绑定（扫码登录） ----

type QRCodeResp struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

// GetQRCode 申请一张机器人绑定二维码（未鉴权）。
func (c *Client) GetQRCode(ctx context.Context) (*QRCodeResp, error) {
	body := map[string]any{
		// 已持有的历史 token 上送后，服务端会识别“已绑定过”（binded_redirect）。
		"local_token_list": []string{},
	}
	data, err := c.postJSON(ctx, "ilink/bot/get_bot_qrcode?bot_type="+botQRType, body, false, callTimeout)
	if err != nil {
		return nil, err
	}
	out := &QRCodeResp{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	if out.QRCode == "" || out.QRCodeImgContent == "" {
		return nil, fmt.Errorf("未获取到二维码")
	}
	return out, nil
}

// QRStatus is one poll of the binding QR state machine.
type QRStatus struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	ILinkBotID   string `json:"ilink_bot_id"`
	ILinkUserID  string `json:"ilink_user_id"`
	BaseURL      string `json:"baseurl"`
	RedirectHost string `json:"redirect_host"`
}

// QR statuses (官方插件 login-qr.ts 的状态机).
const (
	QRWait             = "wait"
	QRScaned           = "scaned"
	QRConfirmed        = "confirmed"
	QRExpired          = "expired"
	QRRedirect         = "scaned_but_redirect"
	QRNeedVerifyCode   = "need_verifycode"
	QRVerifyBlocked    = "verify_code_blocked"
	QRBindedRedirect   = "binded_redirect"
)

// PollQRStatus long-polls one status window.
func (c *Client) PollQRStatus(ctx context.Context, qrcode, verifyCode string) (*QRStatus, error) {
	q := "ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	if verifyCode != "" {
		q += "&verify_code=" + url.QueryEscape(verifyCode)
	}
	data, err := c.get(ctx, q, false, pollTimeout)
	if err != nil {
		return nil, err
	}
	out := &QRStatus{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- 收消息（长轮询） ----

type updatesResp struct {
	Ret                  int       `json:"ret"`
	ErrCode              int       `json:"errcode"`
	ErrMsg               string    `json:"errmsg"`
	Msgs                 []Message `json:"msgs"`
	GetUpdatesBuf        string    `json:"get_updates_buf"`
	LongPollingTimeoutMS int       `json:"longpolling_timeout_ms"`
}

// ErrSessionStale 上报于 errcode=-14：bot_token 失效，需要重新绑定机器人。
func staleSession(resp *updatesResp) bool { return resp.ErrCode == -14 }

// GetUpdates performs one long-poll window. On timeout (no server data) it
// returns the empty response so the caller simply retries.
func (c *Client) GetUpdates(ctx context.Context, buf string) (*updatesResp, error) {
	body := map[string]any{"get_updates_buf": buf}
	timeout := pollTimeout + 5*time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := c.postJSON(cctx, "ilink/bot/getupdates", body, true, timeout)
	if err != nil {
		return nil, err
	}
	out := &updatesResp{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- 发消息 ----

func (c *Client) buildMsg(to, contextToken, runID string, items []MessageItem) Message {
	return Message{
		FromUserID:   "",
		ToUserID:     to,
		ClientID:     randomClientID(),
		MessageType:  msgTypeBot,
		MessageState: stateFinish,
		ItemList:     items,
		ContextToken: contextToken,
		RunID:        runID,
	}
}

type sendResp struct {
	MessageID u64str `json:"message_id"`
	Ret       int    `json:"ret"`
	ErrMsg    string `json:"errmsg"`
}

// SendText sends one text message.
func (c *Client) SendText(ctx context.Context, to, contextToken, text string) error {
	if text == "" {
		return nil
	}
	m := c.buildMsg(to, contextToken, "", []MessageItem{{Type: itemTypeText, TextItem: &MessageTextItem{Text: text}}})
	data, err := c.postJSON(ctx, "ilink/bot/sendmessage", map[string]any{"msg": m}, true, callTimeout)
	if err != nil {
		return err
	}
	out := &sendResp{}
	_ = json.Unmarshal(data, out)
	if out.Ret != 0 {
		return fmt.Errorf("发送失败 ret=%d errmsg=%s", out.Ret, out.ErrMsg)
	}
	return nil
}

// SendImage uploads png to the WeChat CDN (AES-128-ECB) then sends an IMAGE item.
func (c *Client) SendImage(ctx context.Context, to, contextToken, caption string, png []byte) error {
	if caption != "" {
		if err := c.SendText(ctx, to, contextToken, caption); err != nil {
			return err
		}
	}
	item, err := c.imageItem(ctx, to, png)
	if err != nil {
		return err
	}
	m := c.buildMsg(to, contextToken, "", []MessageItem{item})
	data, err := c.postJSON(ctx, "ilink/bot/sendmessage", map[string]any{"msg": m}, true, callTimeout)
	if err != nil {
		return err
	}
	out := &sendResp{}
	_ = json.Unmarshal(data, out)
	if out.Ret != 0 {
		return fmt.Errorf("发送失败 ret=%d errmsg=%s", out.Ret, out.ErrMsg)
	}
	return nil
}

// ---- 生命周期 ----

// NotifyStart 上报客户端启动（最佳效率，失败忽略）。
func (c *Client) NotifyStart(ctx context.Context) error {
	_, err := c.postJSON(ctx, "ilink/bot/msg/notifystart", map[string]any{}, true, 10*time.Second)
	return err
}

// NotifyStop 上报客户端停止（最佳效率，失败忽略）。
func (c *Client) NotifyStop(ctx context.Context) error {
	_, err := c.postJSON(ctx, "ilink/bot/msg/notifystop", map[string]any{}, true, 10*time.Second)
	return err
}
