// Command readtrace runs the ReadTrace dashboard: a single-binary web UI
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
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA database so -trimpath builds still resolve zones

	"readtrace/internal/api"
	"readtrace/internal/checker"
	"readtrace/internal/engine"
	"readtrace/internal/notify"
	"readtrace/internal/sched"
	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/webui"
	"readtrace/internal/weread"
)

var version = "0.2.0"

func main() {
	addr := flag.String("addr", envAny([]string{"READTRACE_ADDR", "WXREAD_ADDR"}, "127.0.0.1:8720"), "监听地址，例如 127.0.0.1:8720 或 0.0.0.0:8720")
	dataDir := flag.String("data", defaultDataDir(), "数据目录（SQLite 与密钥），默认为可执行文件同级的 data 目录")
	noOpen := flag.Bool("no-open", false, "启动后不自动打开浏览器")
	level := flag.String("log-level", envAny([]string{"READTRACE_LOG_LEVEL", "WXREAD_LOG_LEVEL"}, "info"), "日志级别 debug/info/warn/error")
	showVersion := flag.Bool("version", false, "输出版本号")
	resetPassword := flag.Bool("reset-password", false, "清除面板管理密码（下次启动重新设置）")
	retainDays := flag.Int("retain-days", envInt([]string{"READTRACE_RETAIN_DAYS", "WXREAD_RETAIN_DAYS"}, 90),
		"运行记录保留天数（含日志），0 表示永久保留")
	hostAllow := flag.String("host-allow", envAny([]string{"READTRACE_HOST_ALLOW", "WXREAD_HOST_ALLOW"}, ""),
		"允许的 Host 白名单（逗号分隔的主机名），为空不校验；绑定 0.0.0.0 时建议设置")
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

	if err := run(*addr, *dataDir, !*noOpen, *retainDays, *hostAllow, logger); err != nil {
		logger.Error("启动失败", "err", err)
		os.Exit(1)
	}
}

func run(addr, dataDir string, openBrowser bool, retainDays int, hostAllow string, logger *slog.Logger) error {
	lock, err := secure.LockDataDir(dataDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	box, err := secure.New(dataDir)
	if err != nil {
		return fmt.Errorf("初始化加密密钥失败: %w", err)
	}
	st, err := store.Open(dbFilePath(dataDir), box)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.InterruptRunningRuns(); err != nil {
		logger.Warn("清理中断运行记录失败", "err", err)
	}
	_ = st.PurgeSessions()

	// Purge old history now and once a day, so the DB does not grow forever.
	if n, err := st.PurgeHistory(retainDays); err != nil {
		logger.Warn("清理历史运行记录失败", "err", err)
	} else if n > 0 {
		logger.Info("已清理历史运行记录", "runs", n)
	} else if retainDays > 0 {
		logger.Info("历史运行记录保留策略", "retain_days", retainDays)
	}
	go purgeDaily(st, retainDays, logger)

	// Seed defaults on first run.
	if v, _ := st.GetSetting("site_name"); strings.TrimSpace(v) == "" {
		_ = st.SetSetting("site_name", "阅迹")
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
	// 后台账号状态检测：按设置中的间隔自动校验登录态，发现失效可推送提醒。
	// defer 只兜底 run() 提前报错退出的路径；正常关机在下方显式 Wait。
	chk := checker.New(st, eng, client, nt, logger)
	chk.Start()
	defer chk.Wait()

	srv := api.New(st, eng, sch, nt, client, webui.FS(), version, parseHostAllow(hostAllow), logger)
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
	logger.Info("阅迹 · ReadTrace 已启动",
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

	// 关机顺序（保证 StopAll 返回后不再有新运行、退出日志不早于实际等待）：
	// 1. sch.Stop：定时任务先停，不再发起新的启动；
	// 2. eng.StopAll：关闭引擎启动门闩并取消、等待全部运行收尾——此后
	//    Start 一律被拒绝，active 里不会再出现新运行；
	// 3. eng.Hub().CloseAll：先释放 SSE 订阅，HTTP 关闭时不再被长连接阻塞；
	// 4. httpServer.Shutdown：等待在途 HTTP 请求收尾；
	// 5. chk.Wait：停后台检测循环（会先取消正在检查中的请求与失效推送，
	//    再等待 goroutine 全部退出）；
	// 6. 「已安全退出」日志放在 chk.Wait 实际返回之后，不再先于等待打印。
	sch.Stop()
	eng.StopAll()
	eng.Hub().CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	chk.Wait()
	logger.Info("已安全退出")
	return nil
}

// purgeDaily re-runs history retention once a day while the process lives.
func purgeDaily(st *store.Store, retainDays int, logger *slog.Logger) {
	if retainDays <= 0 {
		return
	}
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for range t.C {
		if n, err := st.PurgeHistory(retainDays); err != nil {
			logger.Warn("清理历史运行记录失败", "err", err)
		} else if n > 0 {
			logger.Info("已清理历史运行记录", "runs", n)
		}
	}
}

// parseHostAllow turns a comma-separated allowlist into a normalized list.
func parseHostAllow(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func resetPanelPassword(dataDir string) error {
	lock, err := secure.LockDataDir(dataDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	box, err := secure.New(dataDir)
	if err != nil {
		return err
	}
	st, err := store.Open(dbFilePath(dataDir), box)
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

// defaultDataDir keeps the binary and its data together: unless READTRACE_DATA
// (or the legacy WXREAD_DATA) is set, data lives in the `data` directory next
// to the executable rather than next to the working directory.
func defaultDataDir() string {
	for _, key := range []string{"READTRACE_DATA", "WXREAD_DATA"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return filepath.Join(exeDir(), "data")
}

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Dir(p)
}

func dbFilePath(dataDir string) string {
	newPath := filepath.Join(dataDir, "readtrace.db")
	legacy := filepath.Join(dataDir, "wxread.db")
	if _, err := os.Stat(newPath); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(legacy); err == nil {
			_ = os.Rename(legacy, newPath)
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(legacy + suffix); err == nil {
					_ = os.Rename(legacy+suffix, newPath+suffix)
				}
			}
		}
	}
	return newPath
}

func envAny(keys []string, def string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return def
}

func envInt(keys []string, def int) int {
	v := envAny(keys, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
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
