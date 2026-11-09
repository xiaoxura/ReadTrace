package weread

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFixSyncKeyParsesBusinessCode 转正归档探针
// TestReviewProbeFixSyncKeyIgnoresBusinessFailure（缺陷 23）：
// HTTP 200 携带非零 errCode 时 FixSyncKey 必须报错；登录失效类业务码
// 包 ErrUnauthorized，errors.Is / IsAuthFailure 都能识别。
func TestFixSyncKeyParsesBusinessCode(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"errCode":-2012,"errMsg":"登录超时"}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.Base = srv.URL
	if err := c.FixSyncKey(context.Background(), Cookies{"wr_vid": "1", "wr_skey": "1"}, ""); err == nil {
		t.Fatal("HTTP 200 errCode=-2012 应返回错误")
	} else {
		if !IsAuthFailure(err) {
			t.Fatalf("errCode=-2012 应被判为认证失败: %v", err)
		}
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("errCode=-2012 应包 ErrUnauthorized: %v", err)
		}
		if !strings.Contains(gotPath, "/web/book/chapterInfos") {
			t.Fatalf("endpoint = %q", gotPath)
		}
	}
}

// TestFixSyncKeyNonAuthBusinessCode 非认证类业务码只作普通错误，不包
// ErrUnauthorized（避免被当成登录失效去续期）。
func TestFixSyncKeyNonAuthBusinessCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errCode":-2003,"errMsg":"参数格式错误"}`))
	}))
	defer srv.Close()
	c := NewClient()
	c.Base = srv.URL
	if err := c.FixSyncKey(context.Background(), Cookies{}, "3300060341"); err == nil {
		t.Fatal("errCode=-2003 应返回错误")
	} else if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("-2003 不应包 ErrUnauthorized: %v", err)
	}
}

// TestFixSyncKeySuccessAndDefaults errCode=0 仍返回 nil；缺省 bookID 走
// 默认值。
func TestFixSyncKeySuccessAndDefaults(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 8<<10)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Write([]byte(`{"succ":1,"errCode":0}`))
	}))
	defer srv.Close()
	c := NewClient()
	c.Base = srv.URL
	if err := c.FixSyncKey(context.Background(), Cookies{}, ""); err != nil {
		t.Fatalf("errCode=0 不应报错: %v", err)
	}
	if !strings.Contains(gotBody, "3300060341") {
		t.Fatalf("default bookId body = %q", gotBody)
	}
}

// TestFixSyncKeyHTTP401 传输层 401 也保持包 ErrUnauthorized（doJSON 原有
// 分类，FixSyncKey 原样透传）。
func TestFixSyncKeyHTTP401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := NewClient()
	c.Base = srv.URL
	if err := c.FixSyncKey(context.Background(), Cookies{}, "b"); err == nil {
		t.Fatal("HTTP 401 应返回错误")
	} else if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("HTTP 401 应包 ErrUnauthorized: %v", err)
	} else if !IsAuthFailure(err) {
		t.Fatalf("HTTP 401 应判为认证失败: %v", err)
	}
}
