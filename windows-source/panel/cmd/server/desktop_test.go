package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopConfigStartupUsesPersistedConnection(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer PERSISTED_SYNTHETIC_KEY" {
			t.Error("startup retained stale stdin key")
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer gateway.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(gateway.URL, "http://"))
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gateway-config.json")
	schedule := map[string]bool{}
	for _, name := range []string{"checkin_enabled", "travel_enabled", "activity_enabled", "keepalive_enabled", "school_enabled", "cat_enabled"} {
		schedule[name] = false
	}
	raw, _ := json.Marshal(map[string]any{"listen": "127.0.0.1:" + port, "api_key": "PERSISTED_SYNTHETIC_KEY", "auth_dir": filepath.Join(dir, "auths"), "state_file": filepath.Join(dir, "state.json"), "schedule": schedule})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a1", 32)
	d := &desktopInit{Protocol: 1, InstanceID: "config-startup", Username: "desktop", Password: "SYNTHETIC_PANEL_PASSWORD", GatewayURL: "http://127.0.0.1:1", GatewayKey: "STALE_SYNTHETIC_KEY", AuthDir: filepath.Join(dir, "auths"), ConfigFile: path, BackupDir: filepath.Join(dir, "backups"), PanelControlKey: &key}
	cfg, err := buildDesktopConfig(d)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := buildDesktopHandler(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest("POST", "http://127.0.0.1:9901/api/login", strings.NewReader(`{"username":"desktop","password":"SYNTHETIC_PANEL_PASSWORD"}`))
	login.Header.Set("Origin", "http://127.0.0.1:9901")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, login)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("startup login failed")
	}
	cookie := w.Result().Cookies()[0]
	for _, route := range []string{"/api/config", "/api/models", "/api/session"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:9901"+route, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("startup route %s failed: %d", route, w.Code)
		}
		if strings.Contains(w.Body.String(), "SYNTHETIC_KEY") || strings.Contains(w.Body.String(), dir) {
			t.Fatal("startup route leaked config")
		}
		if route == "/api/session" && !strings.Contains(w.Body.String(), gateway.URL) {
			t.Fatal("session reports stale gateway URL")
		}
	}
}

// TestAssertLoopback 桌面模式只允许回环监听。
//
// 这是防「管理面被暴露到局域网」的最后一道闸：面板能读账号 Token 并改网关配置，
// 一旦绑到全网卡就等于把账号完全控制权开放出去。因此任何非回环地址都必须被拒绝，
// 而不是降级放行。
func TestAssertLoopback(t *testing.T) {
	accept := []string{
		"127.0.0.1:0",
		"127.0.0.1:7863",
		"[::1]:0",
		"127.0.0.2:1234", // 整个 127/8 都是回环
	}
	reject := []struct{ addr, why string }{
		{":8787", "未指定主机 = 全网卡"},
		{":7863", "未指定主机 = 全网卡"},
		{"0.0.0.0:8787", "明确的全网卡"},
		{"192.168.1.10:8787", "局域网地址"},
		{"10.0.0.5:8787", "内网地址"},
		{"example.com:8787", "域名监听"},
		{"localhost:8787", "主机名而非 IP，无法确认是回环"},
		{"[::]:8787", "IPv6 全网卡"},
	}

	for _, addr := range accept {
		if err := assertLoopback(addr); err != nil {
			t.Errorf("assertLoopback(%q) 应通过，却报错：%v", addr, err)
		}
	}
	for _, tc := range reject {
		if err := assertLoopback(tc.addr); err == nil {
			t.Errorf("assertLoopback(%q) 应被拒绝（%s），却通过了", tc.addr, tc.why)
		}
	}
}

// TestValidateDesktopInit 启动参数校验。
func TestValidateDesktopInit(t *testing.T) {
	valid := &desktopInit{
		Protocol:   desktopProtocolVersion,
		InstanceID: "abc123",
		Username:   "desktop",
		GatewayURL: "http://127.0.0.1:7863",
		AuthDir:    "/tmp/auths",
		Password:   strings.Repeat("x", desktopMinPasswordLen),
	}
	if err := validateDesktopInit(valid); err != nil {
		t.Fatalf("合法参数被拒绝：%v", err)
	}

	cases := []struct {
		name   string
		mutate func(*desktopInit)
	}{
		{"协议版本不匹配", func(d *desktopInit) { d.Protocol = 999 }},
		{"缺少 instance_id", func(d *desktopInit) { d.InstanceID = "" }},
		{"instance_id 只有空白", func(d *desktopInit) { d.InstanceID = "   " }},
		{"缺少 username", func(d *desktopInit) { d.Username = "" }},
		{"username 只有空白", func(d *desktopInit) { d.Username = "   " }},
		{"缺少 gateway_url", func(d *desktopInit) { d.GatewayURL = "" }},
		{"gateway_url 只有空白", func(d *desktopInit) { d.GatewayURL = "   " }},
		{"gateway_url 指向远端", func(d *desktopInit) { d.GatewayURL = "http://192.168.1.20:7863" }},
		{"gateway_url 使用域名", func(d *desktopInit) { d.GatewayURL = "http://localhost:7863" }},
		{"gateway_url 缺少端口", func(d *desktopInit) { d.GatewayURL = "http://127.0.0.1" }},
		{"gateway_url 使用 https", func(d *desktopInit) { d.GatewayURL = "https://127.0.0.1:7863" }},
		{"口令过短", func(d *desktopInit) { d.Password = "short" }},
		{"口令为空", func(d *desktopInit) { d.Password = "" }},
		{"缺少 auth_dir", func(d *desktopInit) { d.AuthDir = "" }},
	}
	for _, tc := range cases {
		d := *valid
		tc.mutate(&d)
		if err := validateDesktopInit(&d); err == nil {
			t.Errorf("%s：应被拒绝，却通过了", tc.name)
		}
	}

	if err := validateDesktopInit(nil); err == nil {
		t.Error("nil 参数应被拒绝")
	}
}

func TestValidateDesktopGatewayURL(t *testing.T) {
	accept := []string{
		"http://127.0.0.1:7863",
		"http://127.0.0.2:1234/",
		"http://[::1]:7863",
	}
	for _, raw := range accept {
		if err := validateDesktopGatewayURL(raw); err != nil {
			t.Errorf("validateDesktopGatewayURL(%q) 应通过，却报错：%v", raw, err)
		}
	}

	reject := []string{
		"http://127.0.0.1:7863/v1",
		"http://127.0.0.1:7863?key=1",
		"http://127.0.0.1:7863#fragment",
		"http://user:pass@127.0.0.1:7863",
		"http://example.com:7863",
		"http://192.168.1.20:7863",
		"http://127.0.0.1",
		"http://127.0.0.1:0",
		"http://127.0.0.1:65536",
		"https://127.0.0.1:7863",
	}
	for _, raw := range reject {
		if err := validateDesktopGatewayURL(raw); err == nil {
			t.Errorf("validateDesktopGatewayURL(%q) 应拒绝，却通过了", raw)
		}
	}
}

// TestBuildDesktopConfigIgnoresEnv 桌面模式必须无视 WBGUI_* 环境变量。
//
// 上游存在 WBGUI_LISTEN / WBGUI_PASSWORD / WBGUI_AUTH_DIR 等覆盖通道。
// 父进程环境残留（用户 shell 里 export 过、或从别的部署方式继承）会改写监听地址
// 与内部口令 —— 桌面模式必须免疫，否则「只绑回环」的保证不成立。
func TestBuildDesktopConfigIgnoresEnv(t *testing.T) {
	t.Setenv("WBGUI_LISTEN", ":9999")
	t.Setenv("WBGUI_PASSWORD", "attacker-chosen")
	t.Setenv("WBGUI_USERNAME", "attacker")
	t.Setenv("WBGUI_AUTH_DIR", "/tmp/attacker-auths")
	t.Setenv("WBGUI_CONFIG_FILE", "/tmp/attacker-config.json")
	t.Setenv("WBGUI_GATEWAY_URL", "http://attacker:7863")
	t.Setenv("WBGUI_GATEWAY_API_KEY", "attacker-key")
	t.Setenv("WBGUI_BACKUP_DIR", "/tmp/attacker-backups")
	t.Setenv("WBGUI_CREDENTIALS_FILE", "/tmp/attacker-credentials.json")
	t.Setenv("WBGUI_PRICING_FILE", "/tmp/attacker-pricing.json")
	t.Setenv("WBGUI_CONTAINER", "attacker-container")
	t.Setenv("WBGUI_AUTH_OWNER_UID", "1234")
	t.Setenv("WBGUI_AUTH_OWNER_GID", "1234")
	t.Setenv("WBGUI_SESSION_TTL", "1s")
	t.Setenv("WBGUI_TIMEOUT_SECONDS", "1")
	t.Setenv("WBGUI_READ_ONLY", "true")
	t.Setenv("WBGUI_DANGEROUS_OPS", "true")
	t.Setenv("WBGUI_REFRESH_CONFIG_ON_LOGIN", "true")

	init := &desktopInit{
		Protocol:   desktopProtocolVersion,
		InstanceID: "abc123",
		AuthDir:    "/tmp/legit-auths",
		ConfigFile: "/tmp/legit-gateway.json",
		BackupDir:  "/tmp/legit-backups",
		Username:   "desktop-user",
		Password:   strings.Repeat("p", desktopMinPasswordLen),
		GatewayURL: "http://127.0.0.1:7863",
		GatewayKey: "legit-key",
	}

	cfg, err := buildDesktopConfig(init)
	if err != nil {
		t.Fatalf("buildDesktopConfig 失败：%v", err)
	}

	if cfg.Listen != "127.0.0.1:0" {
		t.Errorf("监听地址被环境变量改写：%q（应为 127.0.0.1:0）", cfg.Listen)
	}
	if cfg.UI.Password != init.Password {
		t.Errorf("内部口令被环境变量改写：%q", cfg.UI.Password)
	}
	if cfg.UI.Username != init.Username {
		t.Errorf("用户名被环境变量改写：%q", cfg.UI.Username)
	}
	if cfg.UpstreamAuthDir != init.AuthDir {
		t.Errorf("凭证目录被环境变量改写：%q", cfg.UpstreamAuthDir)
	}
	if cfg.UpstreamConfigFile != init.ConfigFile {
		t.Errorf("网关配置路径被环境变量改写：%q", cfg.UpstreamConfigFile)
	}
	if cfg.BackupDir != init.BackupDir {
		t.Errorf("备份目录被环境变量改写：%q", cfg.BackupDir)
	}
	if cfg.GatewayURL != init.GatewayURL {
		t.Errorf("网关地址被环境变量改写：%q", cfg.GatewayURL)
	}
	if cfg.GatewayAPIKey != init.GatewayKey {
		t.Errorf("网关密钥被环境变量改写：%q", cfg.GatewayAPIKey)
	}
	if cfg.CredentialsFile != "" {
		t.Errorf("桌面模式不应启用凭据文件：%q", cfg.CredentialsFile)
	}
	if cfg.PricingFile != "" {
		t.Errorf("桌面模式不应启用价格文件：%q", cfg.PricingFile)
	}
	if cfg.TimeoutSeconds != 120 || cfg.UI.SessionTTL != "12h" || cfg.UI.TTL != 12*time.Hour {
		t.Errorf("桌面模式运行时参数被环境变量改写：timeout=%d ttl=%q/%s", cfg.TimeoutSeconds, cfg.UI.SessionTTL, cfg.UI.TTL)
	}
	if cfg.AuthOwnerUID != -1 || cfg.AuthOwnerGID != -1 {
		t.Errorf("桌面模式不应读取凭证文件属主环境变量：%d:%d", cfg.AuthOwnerUID, cfg.AuthOwnerGID)
	}
	if cfg.RefreshConfigOnLogin {
		t.Error("桌面模式不应启用登录后重启")
	}
	if cfg.ReadOnly {
		t.Error("只读标志被环境变量改写")
	}
	if cfg.DangerousOps {
		t.Error("高危操作标志被环境变量改写（必须只由原生层显式下发开启）")
	}
	if cfg.CredentialsFile != "" {
		t.Errorf("桌面模式应关闭网页改口令能力（CredentialsFile 应为空），实际 %q", cfg.CredentialsFile)
	}
	if cfg.DockerContainer != "" {
		t.Errorf("桌面模式不应启用 Docker 重启路径，实际 %q", cfg.DockerContainer)
	}
	if cfg.RefreshConfigOnLogin {
		t.Error("桌面模式不应启用「登录后重启容器」")
	}
	if cfg.AuthOwnerUID != -1 || cfg.AuthOwnerGID != -1 {
		t.Error("桌面模式不应调整凭证文件属主（容器场景专用）")
	}
}

// TestDangerousOpsRequiresExplicitOptIn 高危操作必须显式开启，缺省关闭。
func TestDangerousOpsRequiresExplicitOptIn(t *testing.T) {
	// 环境变量试图打开它 —— 必须无效
	t.Setenv("WBGUI_DANGEROUS_OPS", "true")

	base := desktopInit{
		Protocol:   desktopProtocolVersion,
		InstanceID: "abc123",
		AuthDir:    "/tmp/auths",
		Password:   strings.Repeat("p", desktopMinPasswordLen),
	}

	// 未下发 → 关闭
	cfg, err := buildDesktopConfig(&base)
	if err != nil {
		t.Fatalf("buildDesktopConfig 失败：%v", err)
	}
	if cfg.DangerousOps {
		t.Error("未下发 dangerous_ops 时应为关闭")
	}

	// 显式 false → 关闭
	no := false
	withFalse := base
	withFalse.DangerousOps = &no
	cfg, err = buildDesktopConfig(&withFalse)
	if err != nil {
		t.Fatalf("buildDesktopConfig 失败：%v", err)
	}
	if cfg.DangerousOps {
		t.Error("显式 false 时应为关闭")
	}

	// 显式 true → 开启
	yes := true
	withTrue := base
	withTrue.DangerousOps = &yes
	cfg, err = buildDesktopConfig(&withTrue)
	if err != nil {
		t.Fatalf("buildDesktopConfig 失败：%v", err)
	}
	if !cfg.DangerousOps {
		t.Error("显式 true 时应为开启")
	}
}

// TestBuildDesktopConfigRejectsNonLoopbackListen 兜底断言不会被绕过。
func TestBuildDesktopConfigRejectsNonLoopbackListen(t *testing.T) {
	init := &desktopInit{
		Protocol:   desktopProtocolVersion,
		InstanceID: "abc123",
		AuthDir:    "/tmp/auths",
		Password:   strings.Repeat("p", desktopMinPasswordLen),
	}
	cfg, err := buildDesktopConfig(init)
	if err != nil {
		t.Fatalf("buildDesktopConfig 失败：%v", err)
	}
	if err := assertLoopback(cfg.Listen); err != nil {
		t.Fatalf("桌面配置的监听地址不是回环：%v", err)
	}
}
