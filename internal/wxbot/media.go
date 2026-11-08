// CDN 媒体直传：AES-128-ECB PKCS7 加密 + getuploadurl + 二进制 PUT/POST。
package wxbot

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// randomClientID letters+digits client message id.
func randomClientID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "readtrace-" + hex.EncodeToString(b[:])
}

// toBase64 encodes bytes for data-URI rendering.
func toBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// ecbPKCS7PaddedSize mirrors AES-128-ECB PKCS7 ciphertext length.
func ecbPKCS7PaddedSize(n int) int {
	return ((n / 16) + 1) * 16
}

func pkcs7Pad(b []byte) []byte {
	pad := 16 - len(b)%16
	fill := bytes.Repeat([]byte{byte(pad)}, pad)
	return append(append([]byte{}, b...), fill...)
}

// ecbEncrypt encrypts with AES-128-ECB (PKCS7). Go has no stdlib ECB; the
// cipher is applied block by block, which is exactly ECB semantics.
func ecbEncrypt(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain)
	if len(padded)%16 != 0 {
		return nil, fmt.Errorf("PKCS7 填充结果非 16 字节对齐")
	}
	out := make([]byte, len(padded))
	for off := 0; off < len(padded); off += 16 {
		block.Encrypt(out[off:off+16], padded[off:off+16])
	}
	return out, nil
}

// ecbDecrypt reverses ecbEncrypt (PKCS7 unpad). Kept for inbound media too.
func ecbDecrypt(key, cipher []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(cipher) == 0 || len(cipher)%16 != 0 {
		return nil, fmt.Errorf("密文长度非 16 字节对齐")
	}
	out := make([]byte, len(cipher))
	for off := 0; off < len(cipher); off += 16 {
		block.Decrypt(out[off:off+16], cipher[off:off+16])
	}
	pad := int(out[len(out)-1])
	if pad <= 0 || pad > 16 || pad > len(out) {
		return nil, fmt.Errorf("PKCS7 填充无效")
	}
	return out[:len(out)-pad], nil
}

type uploadURLResp struct {
	UploadParam  string `json:"upload_param"`
	ThumbParam   string `json:"thumb_upload_param"`
	UploadFullURL string `json:"upload_full_url"`
}

// imageItem uploads png and returns the IMAGE MessageItem.
func (c *Client) imageItem(ctx context.Context, toUserID string, png []byte) (MessageItem, error) {
	if len(png) == 0 {
		return MessageItem{}, fmt.Errorf("空图片")
	}
	aesKey := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return MessageItem{}, err
	}
	filekey := make([]byte, 16)
	if _, err := rand.Read(filekey); err != nil {
		return MessageItem{}, err
	}
	cipher, err := ecbEncrypt(aesKey, png)
	if err != nil {
		return MessageItem{}, fmt.Errorf("图片加密失败: %w", err)
	}
	sum := md5.Sum(png)

	req := map[string]any{
		"filekey":       hex.EncodeToString(filekey),
		"media_type":    mediaTypeImg,
		"to_user_id":    toUserID,
		"rawsize":       len(png),
		"rawfilemd5":    hex.EncodeToString(sum[:]),
		"filesize":      len(cipher),
		"no_need_thumb": true,
		"aeskey":        hex.EncodeToString(aesKey),
	}
	data, err := c.postJSON(ctx, "ilink/bot/getuploadurl", req, true, callTimeout)
	if err != nil {
		return MessageItem{}, fmt.Errorf("申请上传地址失败: %w", err)
	}
	resp := &uploadURLResp{}
	if err := json.Unmarshal(data, resp); err != nil {
		return MessageItem{}, err
	}
	target := strings.TrimSpace(resp.UploadFullURL)
	if target == "" {
		if resp.UploadParam == "" {
			return MessageItem{}, fmt.Errorf("CDN 上传地址缺失")
		}
		target = CDNBaseURL + "/upload?encrypted_query_param=" + url.QueryEscape(resp.UploadParam) +
			"&filekey=" + url.QueryEscape(hex.EncodeToString(filekey))
	}
	param, err := c.uploadCiphertext(ctx, target, cipher)
	if err != nil {
		return MessageItem{}, fmt.Errorf("图片上传失败: %w", err)
	}
	item := MessageItem{
		Type: itemTypeImage,
		ImageItem: &MessageImageItem{
			Media: ImageMedia{
				EncryptQueryParam: param,
				AESKey:            base64.StdEncoding.EncodeToString(aesKey),
				EncryptType:       1,
			},
			MidSize: int64(len(cipher)),
		},
	}
	return item, nil
}

// uploadCiphertext POSTs the encrypted buffer and reads back the download param.
func (c *Client) uploadCiphertext(ctx context.Context, target string, cipher []byte) (string, error) {
	uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(uctx, http.MethodPost, target, bytes.NewReader(cipher))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		msg := resp.Header.Get("x-error-message")
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return "", fmt.Errorf("CDN 拒绝：%s", msg)
		}
		return "", fmt.Errorf("CDN 异常：%s", msg)
	}
	param := resp.Header.Get("X-Encrypted-Param")
	if param == "" {
		return "", fmt.Errorf("CDN 未返回下载参数")
	}
	return param, nil
}
