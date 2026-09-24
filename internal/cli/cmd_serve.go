package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ChenYCL/keypoint-notify/internal/httpapi"
	"github.com/ChenYCL/keypoint-notify/internal/store"
	"github.com/ChenYCL/keypoint-notify/internal/webhook"
)

// cmdServe runs the hub: HTTP API, embedded board, event fan-out.
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("kp serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addr := fs.String("addr", "127.0.0.1:8787", "监听地址；0.0.0.0:8787 对外，配合 cloudflared 就保持默认 127.0.0.1")
	data := fs.String("data", "./data", "数据目录：SQLite 与附件都放这里")
	quiet := fs.Bool("quiet", false, "不打印访问日志")
	fs.Usage = func() {
		fmt.Print(`kp serve — 起服务

  kp serve                            127.0.0.1:8787，数据在 ./data
  kp serve --addr 0.0.0.0:8787        对外监听（内网/容器里用）
  kp serve --data ~/.keypoint/data    数据放到指定目录

首次启动会建库、灌入内置角色。之后用 ` + "`kp init`" + ` 连它建身份。

公网访问推荐 cloudflared 隧道而不是直接暴露端口：
  cloudflared tunnel --url http://127.0.0.1:8787
详见 docs/deploy-tunnel.md。
`)
	}
	if err := fs.Parse(intersperse(fs, args)); err != nil {
		return ExitUsage
	}
	if *quiet {
		os.Setenv("KEYPOINT_QUIET", "1")
	}

	dir, err := filepath.Abs(*data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return ExitError
	}
	st, err := store.Open(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗ 打开数据库失败:", err)
		return ExitError
	}
	defer st.Close()
	if err := st.SeedRoles(); err != nil {
		fmt.Fprintln(os.Stderr, "✗ 初始化角色失败:", err)
		return ExitError
	}

	// The skill lives in its own embed package; wire it in rather than making
	// the HTTP layer reach for it directly.
	httpapi.SetSkillFS(skillFS)
	// Serve our own binary so a fresh machine can bootstrap with just curl —
	// resolve the real path rather than trusting os.Args[0], which may be a
	// relative name or a symlink.
	httpapi.SetBinaryProvider(func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	})
	api := httpapi.New(st, Version, webFS)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: /api/v1/stream is a long-lived SSE response and a
		// write deadline would cut it off mid-flight.
		IdleTimeout: 120 * time.Second,
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ 监听 %s 失败: %v\n", *addr, err)
		if strings.Contains(err.Error(), "address already in use") {
			// Naming the holder turns a dead end into a decision: the user can
			// see whether it is a stale kp or an unrelated service.
			if holder := portHolder(*addr); holder != "" {
				fmt.Fprintf(os.Stderr, "  → 端口被占：%s\n", holder)
			}
			fmt.Fprintf(os.Stderr, "  → 换一个端口重试：kp serve --addr 127.0.0.1:%d\n", suggestPort(*addr))
			fmt.Fprintln(os.Stderr, "  → 或者先停掉已有的 kp：pkill -f 'kp serve'")
		}
		return ExitError
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dispatcher := webhook.New(st, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[webhook] "+format+"\n", args...)
	})
	go dispatcher.Run(ctx)

	fmt.Printf("keypoint notify %s\n", Version)
	fmt.Printf("  监听   http://%s\n", displayAddr(*addr))
	fmt.Printf("  数据   %s\n", dir)
	fmt.Printf("  看板   http://%s/\n", displayAddr(*addr))
	fmt.Printf("  API    http://%s/api/v1/llms.txt\n", displayAddr(*addr))

	n, _ := st.CountIdentities()
	if n == 0 {
		fmt.Printf("\n还没有身份。在另一个终端跑 `kp init` 创建第一个（它会认领 admin）。\n")
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, "✗ 服务异常退出:", err)
		return ExitError
	case <-ctx.Done():
		fmt.Println("\n收到停止信号，正在收尾…")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return ExitOK
	}
}

// portHolder shells out to lsof to name whatever is listening on addr. It is
// best-effort: a missing or slow lsof must never block startup.
func portHolder(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return ""
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 2 {
		return ""
	}
	return fmt.Sprintf("%s (pid %s)", fields[0], fields[1])
}

// suggestPort returns a nearby port that is currently free, so the message can
// print a command that will actually work.
func suggestPort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 8877
	}
	base, err := strconv.Atoi(portStr)
	if err != nil {
		return 8877
	}
	for _, candidate := range []int{base + 10, base + 1, base - 1, 8877, 8899, 9000} {
		if candidate <= 1024 || candidate > 65535 {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", candidate))
		if err == nil {
			ln.Close()
			return candidate
		}
	}
	return base + 10
}

// displayAddr turns a wildcard bind into something clickable.
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}
