// Command wxread-panel runs the ReadPilot dashboard: a single-binary web UI
// that keeps WeRead reading sessions alive and visualizes the results.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA database so -trimpath builds still resolve zones

	"wxread-panel/internal/api"
	"wxread-panel/internal/engine"
	"wxread-panel/internal/notify"
	"wxread-panel/internal/sched"
	"wxread-panel/internal/secure"
	"wxread-panel/internal/store"
	"wxread-panel/internal/webui"
	"wxread-panel/internal/weread"
)

var version = "dev"

func main() {
	addr := flag.String("addr", envOr("WXREAD_ADDR", "127.0.0.1:8720"), "监听地址，例如 127.0.0.1:8720 或 0.0.0.0:8720")
	dataDir := flag.String("data", envOr("WXREAD_DATA", "data"), "数据目录（SQLite 与密钥）")
	noOpen := flag.Bool("no-open", false, "启动后不自动打开浏览器")
	level := flag.String("log-level", envOr("WXREAD_LOG_LEVEL", "info"), "日志级别 debug/info/warn/error")
	showVersion := flag.Bool("version", false, "输出版本号")
	resetPassword := flag.Bool("reset-password", false, "清除面板管理密码（下次启动重新设置）")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *resetPassword {
		if err := resetPanelPassword(*dataDir); err != nil {
			fmt.Fprintln(os.Stderr, "重置密码失败:", err)
			os.Exit(1)
		}
		fmt.Println("已清除面板密码，重新启动后进入初始化页面。账号数据保持不变。")
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(*level),
	}))
	slog.SetDefault(logger)

	if err := run(*addr, *dataDir, !*noOpen, logger); err != nil {
		logger.Error("启动失败", "err", err)
		os.Exit(1)
	}
}

func run(addr, dataDir string, openBrowser bool, logger *slog.Logger) error {
	box, err := secure.New(dataDir)
	if err != nil {
		return fmt.Errorf("初始化加密密钥失败: %w", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "wxread.db"), box)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.InterruptRunningRuns(); err != nil {
		logger.Warn("清理中断运行记录失败", "err", err)
	}
	_ = st.PurgeSessions()

	// Seed defaults on first run.
	if v, _ := st.GetSetting("site_name"); strings.TrimSpace(v) == "" {
		_ = st.SetSetting("site_name", "读时")
	}
	tzName, _ := st.GetSetting("timezone")
	if strings.TrimSpace(tzName) == "" {
		tzName = "Asia/Shanghai"
		_ = st.SetSetting("timezone", tzName)
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		logger.Warn("时区无效，回退到 Asia/Shanghai", "timezone", tzName)
		loc, _ = time.LoadLocation("Asia/Shanghai")
		tzName = "Asia/Shanghai"
		_ = st.SetSetting("timezone", tzName)
	}

	client := weread.NewClient()
	hub := engine.NewHub()
	nt := notify.New()
	eng := engine.New(st, client, nt, hub, logger)
	sch := sched.New(st, eng, logger)
	if err := sch.Start(loc); err != nil {
		return fmt.Errorf("启动定时任务失败: %w", err)
	}
	defer sch.Stop()

	srv := api.New(st, eng, sch, nt, client, webui.FS(), version, logger)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败（端口可能被占用）: %w", addr, err)
	}
	url := displayURL(addr)
	logger.Info("读时 · ReadPilot 已启动",
		"version", version, "url", url, "data", absPath(dataDir),
		"go", runtime.Version())
	logger.Info("首次访问需要设置管理密码；登录后可扫码绑定微信读书账号")

	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP 服务异常退出", "err", err)
		}
	}()

	if openBrowser {
		openURL(url)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("收到退出信号，正在停止任务...")

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	eng.StopAll()
	sch.Stop()
	logger.Info("已安全退出")
	return nil
}

func resetPanelPassword(dataDir string) error {
	box, err := secure.New(dataDir)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "wxread.db"), box)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.SetSetting("password_hash", ""); err != nil {
		return err
	}
	return st.PurgeSessions()
}

func displayURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		// Browsers are a convenience; failure is not fatal.
		_ = err
	}
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
