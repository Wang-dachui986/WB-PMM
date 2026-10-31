// Package authstore 读取/写入 workbuddy2api 的账号凭证文件（auths/workbuddy-<uid>.json）。
//
// 磁盘格式与 workbuddy2api/internal/auth 完全一致（嵌套形）：
//
//	{
//	  "account": {"uid","enterpriseId","nickname"},
//	  "auth":    {"accessToken","refreshToken","expiresAt","domain"}
//	}
//
// 兼容读取扁平形（手写/旧版）。写回一律用嵌套形，保证网关与插件都能读。
package authstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workbuddy2api-gui/internal/fsutil"
)

// Account 归一化后的账号凭证。
type Account struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"` // Unix 秒
	Domain       string `json:"domain"`

	UID          string `json:"uid"`
	EnterpriseID string `json:"enterpriseId"`
	Nickname     string `json:"nickname"`

	// FilePath 来源文件绝对路径；仅进程内使用，不序列化到凭证文件。
	FilePath string `json:"-"`
}

// ExpiresAtTime 返回过期时刻。
func (a *Account) ExpiresAtTime() time.Time { return time.Unix(a.ExpiresAt, 0) }

// NeedsRefresh 报告 token 是否将在 within 内过期或已过期。
func (a *Account) NeedsRefresh(within time.Duration) bool {
	if a.ExpiresAt <= 0 {
		return true
	}
	return time.Now().Add(within).Unix() >= a.ExpiresAt
}

// Expired 报告 token 是否已过期。
func (a *Account) Expired() bool { return a.ExpiresAt > 0 && time.Now().Unix() >= a.ExpiresAt }

// uidPattern 校验 uid 形态：UUID 或安全字符集。
// 用于文件名拼接，必须拒绝路径分隔符与 ..，防止穿越写入 auths 目录之外。
var uidPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{6,128}$`)

// ValidUID 报告 uid 是否可安全用于文件名。
func ValidUID(uid string) bool {
	return uidPattern.MatchString(uid) && !strings.Contains(uid, "..")
}

// Store 账号凭证目录访问器。并发安全：所有写操作在进程内互斥。
type Store struct {
	dir         string
	mu          sync.Mutex
	maintenance atomic.Pointer[MaintenanceGate]
	privateDir  string
	privateInfo os.FileInfo

	// ownerUID/ownerGID 落盘后把凭证文件属主改成该 uid/gid（-1 = 不改）。
	//
	// 为什么需要：面板常以 root 运行（要写宿主机挂载的凭证目录），而网关容器
	// 以低权限用户（如 uid 10001 app）读取同一目录。若文件是 root:0600，网关
	// 读不到 → 表现为「账号已添加但池中未加载」。让面板写完后 chown 成网关用户
	// 即可根治，避免用户每次手工 chown。
	ownerUID int
	ownerGID int
}

// EnableMaintenance must be called before desktop HTTP endpoints are served.
// Once installed, callers cannot fall back to unrestricted legacy writes.
func (s *Store) EnableMaintenance(g *MaintenanceGate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g == nil || s.maintenance.Load() != nil {
		return fmt.Errorf("credential maintenance gate already installed or invalid")
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return err
	}
	if err := checkPrivateDirectory(info); err != nil {
		return err
	}
	if err := fsutil.CheckPrivateACL(s.dir); err != nil {
		return err
	}
	s.privateDir, s.privateInfo = real, info
	s.maintenance.Store(g)
	return nil
}

func checkPrivateDirectory(info os.FileInfo) error {
	if !info.IsDir() || (fsutil.PermBitsEnforced() && info.Mode().Perm()&0o077 != 0) {
		return fmt.Errorf("桌面凭证目录必须是真实私有目录（0700）")
	}
	if uid, _, ok := fsutil.FileOwner(info); ok && uid != os.Geteuid() {
		return fmt.Errorf("桌面凭证目录必须属于当前用户")
	}
	return nil
}

func (s *Store) checkDirectory() error {
	if s.maintenance.Load() == nil {
		return nil
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if err := checkPrivateDirectory(info); err != nil {
		return err
	}
	if err := fsutil.CheckPrivateACL(s.dir); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return err
	}
	if real != s.privateDir || !os.SameFile(s.privateInfo, info) {
		return fmt.Errorf("桌面凭证目录已变化")
	}
	return nil
}

func checkWriteContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *Store) credentialWrite(ctx context.Context, kind string, uids []string, write func() error) error {
	// All mutations use Store.mu -> gate.mu. Waiting for the store cannot pin
	// the gate open, and authorization is checked only after obtaining the store.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkWriteContext(ctx); err != nil {
		return err
	}
	if err := s.checkDirectory(); err != nil {
		return err
	}
	if g := s.maintenance.Load(); g != nil {
		return g.withWrite(ctx, kind, uids, write)
	}
	return write()
}

// New 构建 Store；dir 为空时报错（凭证目录是必需配置）。
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("账号凭证目录（auth_dir）未配置")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析凭证目录: %w", err)
	}
	// 默认不改属主（-1/-1）。
	return &Store{dir: abs, ownerUID: -1, ownerGID: -1}, nil
}

// SetOwner 设置落盘后凭证文件的属主（uid/gid 为 -1 表示不改）。
// 用于让网关容器（通常以低权限用户运行）能读到面板写入的凭证。
func (s *Store) SetOwner(uid, gid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ownerUID = uid
	s.ownerGID = gid
}

// Owner 返回当前配置的属主（-1 表示不改）。
func (s *Store) Owner() (uid, gid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownerUID, s.ownerGID
}

// Dir 返回凭证目录绝对路径。
func (s *Store) Dir() string { return s.dir }

// PathFor 返回 uid 对应的凭证文件绝对路径；uid 非法时返回空串。
func (s *Store) PathFor(uid string) string {
	p, err := s.pathFor(uid)
	if err != nil {
		return ""
	}
	return p
}

// pathFor 返回 uid 对应的凭证文件绝对路径（已校验 uid，无穿越风险）。
func (s *Store) pathFor(uid string) (string, error) {
	if !ValidUID(uid) {
		return "", fmt.Errorf("非法 uid：%q", uid)
	}
	return filepath.Join(s.dir, "workbuddy-"+uid+".json"), nil
}

// List 扫描目录下全部 workbuddy-*.json 并按 uid 排序返回。
// 单个文件解析失败不中断：跳过并在第二个返回值里报告，让前端能看到坏文件。
func (s *Store) List() ([]*Account, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

func (s *Store) readAccount(path string) (*Account, error) {
	var raw []byte
	var err error
	if s.maintenance.Load() != nil {
		if err := s.checkDirectory(); err != nil {
			return nil, err
		}
		raw, err = fsutil.ReadFileStrict(path, 1<<20)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	a, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	a.FilePath = path
	return a, nil
}

func (s *Store) list() ([]*Account, []string) {
	if err := s.checkDirectory(); err != nil {
		return nil, []string{err.Error()}
	}
	files, err := filepath.Glob(filepath.Join(s.dir, "workbuddy*.json"))
	if err != nil {
		return nil, []string{fmt.Sprintf("扫描目录失败: %v", err)}
	}
	sort.Strings(files)
	out := make([]*Account, 0, len(files))
	var warnings []string
	for _, f := range files {
		a, err := s.readAccount(f)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", filepath.Base(f), err))
			continue
		}
		a.FilePath = f
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, warnings
}

// Get 按 uid 读取单个账号；不存在返回 os.ErrNotExist 包装错误。
func (s *Store) Get(uid string) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(uid)
}

var ErrCredentialConflict = errors.New("账号凭证冲突，请先处理重复 UID 或不匹配的文件")

func (s *Store) get(uid string) (*Account, error) {
	p, err := s.pathFor(uid)
	if err != nil {
		return nil, err
	}
	if _, err := s.readAccount(p); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	accounts, _ := s.list()
	var found *Account
	for _, a := range accounts {
		if a.UID != uid {
			continue
		}
		if found != nil {
			return nil, ErrCredentialConflict
		}
		found = a
	}
	if found == nil {
		return nil, os.ErrNotExist
	}
	return found, nil
}

// Parse 解析两种磁盘形态：嵌套形（插件 OAuth 输出）与扁平形（手写/旧版）。
func Parse(raw []byte) (*Account, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("凭证文件超过 1 MiB")
	}
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	if len(raw) == 0 {
		return nil, fmt.Errorf("空凭证文件")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("凭证 JSON 解析失败: %w", err)
	}
	var a Account
	if _, nested := probe["auth"]; nested {
		var n struct {
			Auth struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresAt    int64  `json:"expiresAt"`
				Domain       string `json:"domain"`
			} `json:"auth"`
			Account struct {
				UID          string `json:"uid"`
				EnterpriseID string `json:"enterpriseId"`
				Nickname     string `json:"nickname"`
			} `json:"account"`
		}
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("凭证 JSON 解析失败: %w", err)
		}
		a = Account{
			AccessToken:  n.Auth.AccessToken,
			RefreshToken: n.Auth.RefreshToken,
			ExpiresAt:    n.Auth.ExpiresAt,
			Domain:       n.Auth.Domain,
			UID:          n.Account.UID,
			EnterpriseID: n.Account.EnterpriseID,
			Nickname:     n.Account.Nickname,
		}
	} else {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("凭证 JSON 解析失败: %w", err)
		}
	}
	if strings.TrimSpace(a.AccessToken) == "" {
		return nil, fmt.Errorf("缺少 accessToken")
	}
	return &a, nil
}

// Save 原子写回账号凭证（嵌套形，0600 权限）。
// accessToken 为空时拒绝写入——避免用半更新凭证覆盖有效文件。
func (s *Store) Save(a *Account) error {
	return s.SaveContext(context.Background(), a)
}

func (s *Store) SaveContext(ctx context.Context, a *Account) error {
	if a == nil {
		return fmt.Errorf("缺少账号凭证")
	}
	return s.credentialWrite(ctx, "save", []string{a.UID}, func() error { return s.save(ctx, a) })
}

func (s *Store) save(ctx context.Context, a *Account) error {
	if strings.TrimSpace(a.AccessToken) == "" {
		return fmt.Errorf("拒绝写入：accessToken 为空")
	}
	if !ValidUID(a.UID) {
		return fmt.Errorf("拒绝写入：非法 uid %q", a.UID)
	}
	p := a.FilePath
	if p == "" {
		var err error
		if p, err = s.pathFor(a.UID); err != nil {
			return err
		}
		a.FilePath = p
	}
	if filepath.Dir(filepath.Clean(p)) != s.dir || !strings.HasPrefix(filepath.Base(p), "workbuddy") || !strings.HasSuffix(p, ".json") {
		return fmt.Errorf("拒绝写入凭证目录以外的路径")
	}
	doc := map[string]any{
		"account": map[string]any{
			"uid":          a.UID,
			"enterpriseId": a.EnterpriseID,
			"nickname":     a.Nickname,
		},
		"auth": map[string]any{
			"accessToken":  a.AccessToken,
			"refreshToken": a.RefreshToken,
			"expiresAt":    a.ExpiresAt,
			"domain":       a.Domain,
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	validate := func() error {
		if err := checkWriteContext(ctx); err != nil {
			return err
		}
		if err := s.checkDirectory(); err != nil {
			return err
		}
		existing, err := s.readAccount(p)
		if err == nil && existing.UID != a.UID {
			return ErrCredentialConflict
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		accounts, _ := s.list()
		for _, account := range accounts {
			if account.UID == a.UID && account.FilePath != p {
				return ErrCredentialConflict
			}
		}
		return nil
	}
	if err := validate(); err != nil {
		return err
	}
	return s.writeAtomic(ctx, p, raw, validate)
}

// writeAtomic 原子替换（tmp + rename）；在 Docker 单文件挂载等无法 rename 的场景
// 自动回退为原地写入，避免"凭证刷新成功却存不下去"。
// 写入后按 SetOwner 配置调整属主，让网关容器（低权限用户）能读到。
// 调用方必须已持有 s.mu。
func (s *Store) writeAtomic(ctx context.Context, path string, raw []byte, validate func() error) error {
	if s.maintenance.Load() != nil {
		return fsutil.WriteFileAtomicStrict(ctx, path, raw, 0o600, validate)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建目录: %w", err)
	}
	if _, err := fsutil.WriteFileAtomic(path, raw, 0o600); err != nil {
		return err
	}
	// 属主调整：仅当显式配置过（>=0）才生效。
	// 失败不视为写凭证失败（凭证内容已落盘），但必须让调用方能感知——
	// 因此返回错误，由上层决定是否降级为告警。
	if s.ownerUID >= 0 || s.ownerGID >= 0 {
		uid, gid := s.ownerUID, s.ownerGID
		if uid < 0 {
			uid = -1
		}
		if gid < 0 {
			gid = -1
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("凭证已写入，但调整文件属主为 %d:%d 失败（网关可能读不到该账号）: %w", uid, gid, err)
		}
		// 目录也要可进入：网关需要 stat/open 该目录下的文件。
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("凭证已写入，但调整目录权限失败: %w", err)
		}
	}
	return nil
}

// ErrNoCredentialFile 表示该账号没有本地凭证文件可删除。
var ErrNoCredentialFile = errors.New("该账号没有本地凭证文件（面板挂载的凭证目录可能与网关不一致）")

// Delete 删除账号凭证文件。
func (s *Store) Delete(uid string) error {
	return s.DeleteContext(context.Background(), uid)
}

func (s *Store) DeleteContext(ctx context.Context, uid string) error {
	return s.credentialWrite(ctx, "delete", []string{uid}, func() error { return s.delete(ctx, uid) })
}

func (s *Store) delete(ctx context.Context, uid string) error {
	if _, err := s.pathFor(uid); err != nil {
		return err
	}
	paths := map[string]bool{}
	accounts, _ := s.list()
	for _, a := range accounts {
		if a.UID == uid {
			paths[a.FilePath] = true
		}
	}
	removed := false
	for path := range paths {
		if err := s.checkDirectory(); err != nil {
			return err
		}
		account, err := s.readAccount(path)
		if err != nil {
			return err
		}
		if account.UID != uid {
			return ErrCredentialConflict
		}
		if err := checkWriteContext(ctx); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		removed = true
	}
	if !removed {
		return ErrNoCredentialFile
	}
	return nil
}

// Rename 把 oldUID 的凭证文件改名为 newUID（用于修正 uid）。
func (s *Store) Rename(oldUID, newUID string) error {
	return s.RenameContext(context.Background(), oldUID, newUID)
}

func (s *Store) RenameContext(ctx context.Context, oldUID, newUID string) error {
	return s.credentialWrite(ctx, "rename", []string{oldUID, newUID}, func() error { return s.rename(ctx, oldUID, newUID) })
}

func (s *Store) rename(ctx context.Context, oldUID, newUID string) error {
	old, err := s.get(oldUID)
	if err != nil {
		return err
	}
	newPath, err := s.pathFor(newUID)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(newPath); err == nil {
		return fmt.Errorf("目标账号 %s 已存在", newUID)
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := s.get(newUID); err == nil {
		return ErrCredentialConflict
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := s.checkDirectory(); err != nil {
		return err
	}
	if err := checkWriteContext(ctx); err != nil {
		return err
	}
	if err := os.Rename(old.FilePath, newPath); err != nil {
		return err
	}
	return nil
}
