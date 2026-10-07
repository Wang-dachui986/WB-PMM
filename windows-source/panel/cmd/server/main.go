// main.go workbuddy2api Web GUI 入口：装配配置、凭证存储、网关/上游客户端与 HTTP 服务。
//
// 两种启动方式：
//   - 默认（服务器 / Docker 部署）：读配置文件 + WBGUI_* 环境变量，绑配置里的固定端口。
//   - --desktop（桌面应用内嵌）：由原生壳通过私有 stdin 管道下发参数，绑 127.0.0.1:0
//     并回报实际端口。全部差异集中在 desktop.go，本文件只留一个分支。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"workbuddy2api-gui/internal/api"
	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/config"
	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/ops"
	"workbuddy2api-gui/internal/upstream"
	"workbuddy2api-gui/internal/webui"
)

// version 构建期注入：-ldflags "-X main.version=..."
var version = "dev"

func main() {
	cfgPath := flag.String("config", "config.json", "GUI 配置文件路径（不存在则用默认值 + WBGUI_* 环境变量）")
	printVersion := flag.Bool("version", false, "打印版本后退出")
	desktop := flag.Bool("desktop", false, "桌面模式：由原生壳通过 stdin 管道驱动（不读 WBGUI_* 环境变量）")
	flag.Parse()

	if *printVersion {
		log.Printf("workbuddy2api-gui %s", version)
		return
	}

	if *desktop {
		os.Exit(runDesktop(version))
	}

	runServer(*cfgPath)
}

// runServer 服务器 / 容器部署路径。行为与上游一致。
func runServer(cfgPath string) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	// 网页改密码的持久化：若存在凭据文件，覆盖内存中的用户名/密码。
	cfg.LoadStoredCredentials()

	// 凭证目录：不存在时创建（首次部署常见：网关还没登录过任何账号）。
	store, err := authstore.New(cfg.UpstreamAuthDir)
	if err != nil {
		log.Fatalf("初始化凭证存储失败: %v", err)
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		log.Printf("警告：创建凭证目录 %s 失败: %v", store.Dir(), err)
	}
	// 凭证文件属主：容器部署时设成网关容器的运行用户，否则网关读不到面板写入的账号
	// （表现为「账号已添加但池中未加载」）。
	if cfg.AuthOwnerUID >= 0 || cfg.AuthOwnerGID >= 0 {
		store.SetOwner(cfg.AuthOwnerUID, cfg.AuthOwnerGID)
		log.Printf("凭证文件属主将设为 %d:%d（供网关容器读取）", cfg.AuthOwnerUID, cfg.AuthOwnerGID)
	}

	handler, err := buildHandler(cfg, store)
	if err != nil {
		log.Fatalf("%v", err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		// 不设 WriteTimeout：聊天测试台的 SSE 流可能长达数分钟。
		IdleTimeout: 120 * time.Second,
	}

	ctx, stop := signalContext()
	defer stop()

	// 启动自检：把关键上下文打成日志，便于排查部署问题。
	log.Printf("workbuddy2api-gui %s 启动中", version)
	log.Printf("  面板监听:   %s", cfg.Listen)
	log.Printf("  面板账号:   %s", cfg.UI.Username)
	log.Printf("  网关地址:   %s", cfg.GatewayURL)
	log.Printf("  凭证目录:   %s", store.Dir())
	log.Printf("  网关配置:   %s", cfg.UpstreamConfigFile)
	log.Printf("  容器名:     %s（高危操作=%v，只读=%v）", orNone(cfg.DockerContainer), cfg.DangerousOps, cfg.ReadOnly)
	if cfg.UsingDefaultPassword() {
		log.Printf("  ⚠️  正在使用默认口令（admin/workbuddy），请尽快在配置文件或 WBGUI_PASSWORD 中修改")
	}
	if hc, err := gatewayHealth(ctx, cfg); err == nil {
		log.Printf("  网关健康:   OK（账号 %d/%d 可用）", hc.Healthy, hc.Total)
	} else {
		log.Printf("  网关健康:   不可达 —— %v", err)
	}

	go func() {
		<-ctx.Done()
		log.Printf("收到退出信号，正在停止…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("停机超时: %v", err)
		}
	}()

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP 服务失败: %v", err)
	}
	log.Printf("已退出")
}

// buildHandler 装配凭证存储、网关/上游客户端与 HTTP handler。
// 服务器路径与桌面路径共用，避免两处装配逻辑漂移。
func buildHandler(cfg *config.Config, store *authstore.Store) (http.Handler, error) {
	return buildHandlerMode(cfg, store, false)
}

func buildHandlerMode(cfg *config.Config, store *authstore.Store, desktop bool) (http.Handler, error) {
	// 网关 api_key：未显式配置时从上游 config.json 自动读取（单机部署零配置体验）。
	if cfg.GatewayAPIKey == "" {
		if key := readGatewayKey(cfg.UpstreamConfigFile); key != "" {
			cfg.GatewayAPIKey = key
			log.Printf("已从 %s 读取网关 api_key", cfg.UpstreamConfigFile)
		} else {
			log.Printf("警告：未能读取网关 api_key，若网关设置了鉴权，账号页与聊天台会报 401")
		}
	}

	gw := gateway.New(cfg.GatewayURL, func() string { return cfg.GatewayAPIKey }, cfg.Timeout())
	up := upstream.New(cfg.Timeout())
	svc := ops.New(cfg, store, gw, up)

	static, err := webui.Handler()
	if err != nil {
		return nil, err
	}
	var srv *api.Server
	if desktop {
		srv = api.NewDesktopServer(cfg, svc, static, version)
	} else {
		srv = api.NewServer(cfg, svc, static, version)
	}
	return srv.Handler(), nil
}

// buildHandlerFromConfig 桌面路径入口：自建凭证存储后装配 handler。
func buildHandlerFromConfig(cfg *config.Config) (http.Handler, error) {
	store, err := authstore.New(cfg.UpstreamAuthDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		return nil, err
	}
	return buildHandlerMode(cfg, store, true)
}

// signalContext 返回在 SIGINT / SIGTERM 时取消的上下文。
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// gatewayHealth 探活网关（仅用于启动日志，失败不致命）。
func gatewayHealth(ctx context.Context, cfg *config.Config) (*gateway.Health, error) {
	gw := gateway.New(cfg.GatewayURL, func() string { return cfg.GatewayAPIKey }, cfg.Timeout())
	return gw.Health(ctx)
}

// readGatewayKey 从网关 config.json 读取 api_key。
func readGatewayKey(path string) string {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		APIKey string `json:"api_key"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	return doc.APIKey
}

func orNone(s string) string {
	if s == "" {
		return "（未配置）"
	}
	return s
}
