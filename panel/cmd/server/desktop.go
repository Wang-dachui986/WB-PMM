// desktop.go 桌面模式（--desktop）：由原生壳通过私有管道驱动的启动路径。
//
// 为什么单独一个文件：桌面模式与「服务器/Docker 部署」在启动契约上完全不同 ——
// 前者由父进程通过 stdin 传入随机内部口令与显式数据目录，监听 127.0.0.1:0 并把
// 实际端口回报给父进程；后者读配置文件、绑固定端口、日志即人读。把差异集中在这里，
// main.go 只留一个分支，上游原有的服务器行为一行不改。
//
// 安全前提（对应开发计划 §6.1、§7.1）：
//   - 只绑回环地址。任何非回环监听一律拒绝启动，不静默降级。
//   - 内部口令只走 stdin 管道，不进命令行、不进 URL、不进日志。
//   - 桌面模式**忽略 WBGUI_* 环境变量**：上游存在 WBGUI_LISTEN / WBGUI_PASSWORD 等
//     覆盖通道，父环境残留会改写监听地址与口令。桌面覆盖在 config.Load 之后应用，
//     因此始终胜出。
//   - 关闭网页改口令能力（CredentialsFile 置空），内部口令每次启动重新生成。
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"workbuddy2api-gui/internal/api"
	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/config"
	"workbuddy2api-gui/internal/fsutil"
	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/ops"
	"workbuddy2api-gui/internal/upstream"
	"workbuddy2api-gui/internal/webui"
)

const (
	// desktopProtocolVersion 原生层与面板之间的启动契约版本。
	// 不兼容变更时必须递增，并在原生层做双向校验。
	desktopProtocolVersion = 1

	// readyPrefix stdout 上可识别的握手前缀。原生层按行扫描寻找它。
	// 选用不会被日志自然产生的前缀，避免误匹配。
	readyPrefix = "@@WBGUI-READY@@ "

	// desktopInitTimeout 等待原生层下发启动参数的上限。
	desktopInitTimeout = 10 * time.Second

	// desktopMinPasswordLen 内部口令最小长度。它是随机生成的，这里只做兜底校验，
	// 防止原生层出 bug 时用空口令/弱口令把面板暴露在回环上。
	desktopMinPasswordLen = 16

	// desktopShutdownGrace 收到退出信号后等待在途请求结束的上限。
	desktopShutdownGrace = 5 * time.Second
)

// desktopInit 原生层通过私有 stdin 管道传入的启动参数。
//
// 全部字段都来自父进程，面板不读任何配置文件来补齐 —— 桌面模式下的数据目录、
// 网关地址、凭证目录必须由原生层显式指定，避免依赖工作目录或历史残留。
type desktopInit struct {
	Protocol        int     `json:"protocol"`
	InstanceID      string  `json:"instance_id"`
	GatewayURL      string  `json:"gateway_url"`
	AuthDir         string  `json:"auth_dir"`
	ConfigFile      string  `json:"config_file"`
	BackupDir       string  `json:"backup_dir"`
	GatewayKey      string  `json:"gateway_api_key"`
	Username        string  `json:"username"`
	Password        string  `json:"password"`
	PanelConfig     string  `json:"panel_config"`
	PanelControlKey *string `json:"panel_control_key"`

	// DangerousOps 解锁高危操作（解禁账号 / 删除凭证）。
	//
	// 用指针而非 bool：需要区分「原生层没下发」与「原生层显式下发 false」。
	// 未下发时一律按 false 处理 —— 高危能力必须显式开启，不能靠环境变量或默认值溜进来。
	DangerousOps *bool `json:"dangerous_ops"`
}

// desktopReady 回报给原生层的机器可读就绪消息。
//
// 明确**不含任何密钥**：内部口令、网关 api_key 都不出现在这里。
type desktopReady struct {
	Service    string `json:"service"`
	Protocol   int    `json:"protocol"`
	Version    string `json:"version"`
	Port       int    `json:"port"`
	InstanceID string `json:"instance_id"`
	PID        int    `json:"pid"`
}

// runDesktop 桌面模式主流程。返回进程退出码。
func runDesktop(version string) int {
	// 普通日志一律走 stderr，stdout 只留给握手消息，避免污染机器可读输出。
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	stdin := bufio.NewReader(os.Stdin)

	init, err := readDesktopInit(stdin)
	if err != nil {
		log.Printf("桌面模式：读取启动参数失败: %v", err)
		return 2
	}
	if err := validateDesktopInit(init); err != nil {
		log.Printf("桌面模式：启动参数非法: %v", err)
		return 2
	}

	cfg, err := buildDesktopConfig(init)
	if err != nil {
		log.Printf("桌面模式：构建配置失败: %v", err)
		return 2
	}

	// 显式建 listener：只有这样才能拿到系统分配的真实端口（:0 场景）。
	// 回环地址在此写死，不接受任何来源的覆盖。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("桌面模式：无法在回环地址上监听: %v", err)
		return 3
	}
	port, ok := listenPort(ln)
	if !ok {
		_ = ln.Close()
		log.Printf("桌面模式：无法解析监听端口")
		return 3
	}

	handler, err := buildDesktopHandler(cfg, init)
	if err != nil {
		_ = ln.Close()
		log.Printf("桌面模式：装配服务失败: %v", err)
		return 3
	}

	httpSrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		// 不设 WriteTimeout：聊天测试台的 SSE 流可能长达数分钟。
		IdleTimeout: 120 * time.Second,
	}

	// 退出通道：stdin EOF（父进程消失）或信号。
	// stdin EOF 是 T06 的核心 —— 父应用被强杀时管道关闭，子进程必须自己退出，
	// 否则留下长期孤儿服务。
	ctx, stop := signalContext()
	defer stop()
	parentGone := watchParentGone(stdin)

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			log.Printf("收到退出信号，正在停止…")
		case <-parentGone:
			log.Printf("父进程管道已关闭，正在停止…")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), desktopShutdownGrace)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("停机超时: %v", err)
			_ = httpSrv.Close()
		}
	}()

	// 就绪握手必须在 Serve 之前写出：原生层拿到端口后立刻会去登录，
	// 若此时还没开始 Serve，登录会撞上连接拒绝。
	if err := emitDesktopReady(desktopReady{
		Service:    "wbgui",
		Protocol:   desktopProtocolVersion,
		Version:    version,
		Port:       port,
		InstanceID: init.InstanceID,
		PID:        os.Getpid(),
	}); err != nil {
		_ = ln.Close()
		log.Printf("桌面模式：输出就绪消息失败: %v", err)
		return 4
	}

	log.Printf("workbuddy2api-gui %s（桌面模式）已在 127.0.0.1:%d 就绪", version, port)

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("桌面模式：HTTP 服务失败: %v", err)
		return 5
	}
	<-shutdownDone
	log.Printf("已退出")
	return 0
}

// readDesktopInit 从 stdin 读取一行 JSON 启动参数，带超时。
func readDesktopInit(r *bufio.Reader) (*desktopInit, error) {
	type result struct {
		init *desktopInit
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		var init desktopInit
		// 用 Decoder 而非 ReadString：不要求原生层严格单行，也便于未来扩展字段。
		if err := json.NewDecoder(r).Decode(&init); err != nil {
			ch <- result{nil, err}
			return
		}
		ch <- result{&init, nil}
	}()

	select {
	case res := <-ch:
		return res.init, res.err
	case <-time.After(desktopInitTimeout):
		return nil, fmt.Errorf("等待启动参数超时（%s）", desktopInitTimeout)
	}
}

// watchParentGone 在初始化消息读完后继续监听 stdin；返回的通道在父进程消失时关闭。
//
// 注意：初始化消息可能被 Decoder 缓冲了额外字节，因此必须复用同一个 bufio.Reader，
// 否则会漏掉紧随其后的 EOF。
func watchParentGone(r *bufio.Reader) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 父进程存活期间不会有更多输入，这里会一直阻塞；
		// 父进程退出（正常或被杀）时管道关闭，Read 返回 EOF。
		_, _ = io.Copy(io.Discard, r)
	}()
	return done
}

// validateDesktopInit 校验原生层下发的参数。
func validateDesktopInit(init *desktopInit) error {
	if init == nil {
		return errors.New("启动参数为空")
	}
	if init.Protocol != desktopProtocolVersion {
		return fmt.Errorf("协议版本不匹配：原生层 %d，面板 %d", init.Protocol, desktopProtocolVersion)
	}
	if strings.TrimSpace(init.InstanceID) == "" {
		return errors.New("缺少 instance_id")
	}
	if strings.TrimSpace(init.Username) == "" {
		return errors.New("缺少 username")
	}
	if strings.TrimSpace(init.GatewayURL) == "" {
		return errors.New("缺少 gateway_url")
	}
	if err := validateDesktopGatewayURL(init.GatewayURL); err != nil {
		return err
	}
	if len(init.Password) < desktopMinPasswordLen {
		return fmt.Errorf("内部口令长度不足 %d 位", desktopMinPasswordLen)
	}
	if init.PanelControlKey != nil {
		key := *init.PanelControlKey
		raw, err := hex.DecodeString(key)
		if err != nil || len(raw) != 32 || key == init.Password || key == init.GatewayKey {
			return errors.New("panel_control_key 必须是独立的 32 字节十六进制密钥")
		}
	}
	if strings.TrimSpace(init.AuthDir) == "" {
		return errors.New("缺少 auth_dir（凭证目录必须由原生层显式指定）")
	}
	return nil
}

// buildDesktopHandler installs the gate before any HTTP request can arrive.
// Private stdin settings never pass through public Config or environment values.
func buildDesktopHandler(cfg *config.Config, init *desktopInit) (http.Handler, error) {
	store, err := authstore.New(cfg.UpstreamAuthDir)
	if err != nil {
		return nil, err
	}
	if err := fsutil.EnsurePrivateDir(store.Dir()); err != nil {
		return nil, err
	}
	gw := gateway.New(cfg.GatewayURL, func() string { return cfg.GatewayAPIKey }, cfg.Timeout())
	svc := ops.New(cfg, store, gw, upstream.New(cfg.Timeout()))
	static, err := webui.Handler()
	if err != nil {
		return nil, err
	}
	srv := api.NewDesktopServer(cfg, svc, static, version)
	key := ""
	if init.PanelControlKey != nil {
		key = *init.PanelControlKey
	}
	if err := srv.ConfigureDesktopMaintenance(init.InstanceID, key); err != nil {
		return nil, err
	}
	if cfg.UpstreamConfigFile != "" {
		if err := svc.EnableDesktopConfig(); err != nil {
			return nil, err
		}
	}
	return srv.Handler(), nil
}

// buildDesktopConfig 组装桌面模式配置。
//
// 全部安全相关字段在此**硬覆盖**，且发生在 config.Load 之后 —— 上游的 WBGUI_*
// 环境变量覆盖通道因此失效。这不是可选项：父环境残留（例如用户 shell 里导出过
// WBGUI_LISTEN=:8787）会让面板监听到全网卡，等于把能读账号 Token 的管理面暴露出去。
func buildDesktopConfig(init *desktopInit) (*config.Config, error) {
	cfg, err := config.Load(init.PanelConfig)
	if err != nil {
		// 面板配置读失败不是致命错误：桌面模式所需的一切都由原生层显式给定。
		// 但仍要告知，便于排查「为什么设置没生效」。
		log.Printf("警告：读取面板配置 %s 失败，改用桌面默认值: %v", init.PanelConfig, err)
		cfg = config.Default()
	}

	// 数据目录与网关地址：一律以原生层下发为准。所有可由 WBGUI_* 覆盖的字段
	// 都在这里重置，避免 config.Load 的环境变量优先级穿透桌面启动契约。
	cfg.UpstreamAuthDir = init.AuthDir
	cfg.UpstreamConfigFile = init.ConfigFile
	cfg.BackupDir = init.BackupDir
	cfg.GatewayURL = strings.TrimRight(strings.TrimSpace(init.GatewayURL), "/")
	cfg.GatewayAPIKey = init.GatewayKey

	// 内部凭据：每次启动由原生层随机生成。
	cfg.UI.Username = init.Username
	cfg.UI.Password = init.Password
	cfg.UI.SessionTTL = "12h"
	cfg.UI.TTL = 12 * time.Hour

	// 桌面模式必须关闭的能力：
	//   CredentialsFile 置空 → handleChangePassword 直接返回 not_supported，
	//   网页改口令入口自然失效（前端据 /api/session 的 password_changeable 隐藏它）。
	cfg.CredentialsFile = ""
	cfg.PricingFile = ""
	// 桌面版自带服务生命周期，不使用 Docker 重启路径。
	cfg.DockerContainer = ""
	cfg.RefreshConfigOnLogin = false
	// 凭证文件属主调整是容器场景的需求，桌面下不适用。
	cfg.AuthOwnerUID = -1
	cfg.AuthOwnerGID = -1
	// 桌面版有原生维护流程（WB-04），不靠全局只读开关兜底。
	cfg.ReadOnly = false
	// 高危操作必须由原生层显式下发才开启：解禁账号 / 删除凭证不可通过上游 API 撤销，
	// 让 WBGUI_DANGEROUS_OPS 这类环境变量残留把它打开是不可接受的。
	cfg.DangerousOps = init.DangerousOps != nil && *init.DangerousOps
	cfg.TimeoutSeconds = 120

	// 监听地址：回环 + 系统分配端口。不读配置、不读环境变量。
	cfg.Listen = "127.0.0.1:0"

	if err := assertLoopback(cfg.Listen); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateDesktopGatewayURL 只允许桌面壳管理的本机网关地址。
// GatewayAPIKey 会随网关请求发送，远端或带 URL 附加部分的地址都不能进入桌面配置。
func validateDesktopGatewayURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("gateway_url 无法解析: %w", err)
	}
	if u.Scheme != "http" {
		return errors.New("gateway_url 只允许使用 http")
	}
	if u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("gateway_url 不允许携带用户信息、查询参数或片段")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("gateway_url 不允许携带路径")
	}
	if !loopbackHostPort(u.Host) {
		return errors.New("gateway_url 只允许带明确端口的回环 IP 地址")
	}
	return nil
}

// loopbackHostPort 校验 host 是带合法端口的字面回环 IP。
func loopbackHostPort(host string) bool {
	ipText, portText, err := net.SplitHostPort(host)
	if err != nil || ipText == "" || portText == "" {
		return false
	}
	ip := net.ParseIP(ipText)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port > 0 && port <= 65535
}

// assertLoopback 拒绝一切非回环监听地址。
//
// 桌面模式不做「降级放行」：宁可启动失败让用户看到明确错误，也不能悄悄把管理面
// 暴露到局域网。
func assertLoopback(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("监听地址 %q 无法解析: %w", listen, err)
	}
	if host == "" {
		return fmt.Errorf("监听地址 %q 未指定主机，等价于全网卡监听，桌面模式禁止", listen)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("监听地址 %q 不是 IP，桌面模式只允许回环地址", listen)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("监听地址 %q 不是回环地址，桌面模式只允许 127.0.0.1", listen)
	}
	return nil
}

// listenPort 取 listener 的实际端口。
func listenPort(ln net.Listener) (int, bool) {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, false
	}
	return addr.Port, true
}

// emitDesktopReady 把就绪消息写到 stdout。
//
// **不需要也不能 flush。** Go 的 `os.Stdout` 是无缓冲的，每次 `Write` 直接落到系统调用，
// 不存在「留在缓冲区里没送出去」的可能。
//
// 这里曾经调用 `os.Stdout.Sync()` 作为「立即送达」的保险，结果是：
// fsync 在**管道**上不被支持，macOS 上实测返回
// `sync /dev/stdout: bad file descriptor`。于是出现
// 「消息已经成功写出、原生层也收到了，面板进程却因为 Sync 报错立即退出」的诡异现象 ——
// 从原生侧看只是「面板刚就绪就挂了」，完全看不出原因。
//
// 更隐蔽的是：用「stdout 重定向到文件」做的手检脚本不会暴露它（fsync 对普通文件有效），
// 只有把 stdout 接成管道 —— 也就是原生壳真实的 `Process` + `Pipe` 拓扑 —— 才会复现。
// 因此 scripts/verify-desktop-handshake.sh 已改为用真管道驱动。
func emitDesktopReady(ready desktopReady) error {
	raw, err := json.Marshal(ready)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "%s%s\n", readyPrefix, raw)
	return err
}
