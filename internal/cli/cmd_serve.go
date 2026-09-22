package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/light/keypoint-notify/internal/httpapi"
	"github.com/light/keypoint-notify/internal/store"
	"github.com/light/keypoint-notify/internal/webhook"
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
			fmt.Fprintln(os.Stderr, "  → 已经有一个 kp serve 在跑？用 `lsof -i :8787` 看看，或换 --addr")
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
