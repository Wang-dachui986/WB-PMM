// login.go 网页版 OAuth 设备授权登录：把 login.sh 的多账号登录流程搬进 GUI。
//
// 流程（与 cmd/login 一致，无 PKCE）：
//
//	① Start   → POST /v2/plugin/auth/state 拿 state + authUrl，创建会话
//	② 用户在浏览器打开 authUrl 完成登录
//	③ Poll    → GET /v2/plugin/auth/token?state= 轮询；成功后自动拉取 uid/nickname
//	④ Finish  → 落盘 auths/workbuddy-<uid>.json，可选重启网关容器加载
package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/upstream"
)

// LoginState 登录会话当前阶段。
type LoginState string

const (
	// LoginPending 已生成授权 URL，等待用户在浏览器完成登录。
	LoginPending LoginState = "pending"
	// LoginSuccess 已成功拿到凭证。
	LoginSuccess LoginState = "success"
	// LoginError 登录过程出错。
	LoginError LoginState = "error"
	// LoginExpired 会话超过 TTL 未完成。
	LoginExpired LoginState = "expired"
	// LoginCancelled 用户主动取消。
	LoginCancelled LoginState = "cancelled"
)

// loginSessionTTL 登录会话存活时长（超过即视为过期，避免 state 长期滞留）。
const loginSessionTTL = 15 * time.Minute

// LoginSession 一次设备授权登录会话。
type LoginSession struct {
	ID        string          `json:"id"`
	State     string          `json:"-"`      // 上游 state：不下发给前端，避免被误用
	Region    upstream.Region `json:"region"` // cn | global
	AuthURL   string          `json:"auth_url"`
	Status    LoginState      `json:"status"`
	Message   string          `json:"message,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`

	// 成功后填充。
	UID                           string `json:"uid,omitempty"`
	Nickname                      string `json:"nickname,omitempty"`
	Saved                         bool   `json:"saved"` // 凭证是否已落盘
	File                          string `json:"file,omitempty"`
	Restart                       string `json:"restart,omitempty"` // 容器重启结果说明
	RequiresOverwriteConfirmation bool   `json:"requires_overwrite_confirmation,omitempty"`

	// 已弹过的过期提醒只发一次，避免前端重复提示。
	notifiedExpiry bool

	// acct 登录成功后暂存的凭证，等待 Service 落盘后清除（不下发前端）。
	acct          *authstore.Account
	ctx           context.Context
	cancel        context.CancelFunc
	release       func()
	stop          func() bool
	busy          bool
	claim         maintenanceClaim
	overwriteUID  string
	credentialErr error
}

// CredentialError exposes typed save failures without parsing a display message.
func (s *LoginSession) CredentialError() error { return s.credentialErr }

// LoginManager 管理进行中的登录会话。
type LoginManager struct {
	up      *upstream.Client
	desktop bool

	mu       sync.Mutex
	sessions map[string]*LoginSession
	// activeID 当前进行中的登录会话 id（同一时间只允许一个，避免多账号登录串会话）。
	activeID string
}

// NewLoginManager 构建登录管理器。
func NewLoginManager(up *upstream.Client) *LoginManager {
	return &LoginManager{up: up, sessions: map[string]*LoginSession{}}
}

var ErrLoginBusy = fmt.Errorf("已有登录操作正在进行")
var ErrLoginInternal = errors.New("登录操作内部错误")
var ErrLoginOverwriteNotPending = errors.New("登录会话没有待确认的匹配账号")
var ErrLoginNotFound = errors.New("登录会话不存在或已过期")

// Start preserves the ordinary server entry point. Desktop must use Service
// StartLoginContext so admission is registered before network IO.
func (m *LoginManager) Start(region upstream.Region) (*LoginSession, error) {
	if m.desktop {
		return nil, authstore.ErrMaintenanceRequired
	}
	return m.start(context.Background(), region, func() {})
}

func (m *LoginManager) start(ctx context.Context, region upstream.Region, release func()) (out *LoginSession, resultErr error) {
	id, err := randomID()
	if err != nil {
		release()
		return nil, err
	}
	m.mu.Lock()
	m.gcLocked(time.Now())
	if m.activeID != "" {
		m.mu.Unlock()
		release()
		return nil, ErrLoginBusy
	}
	if ctx.Err() != nil {
		m.mu.Unlock()
		release()
		return nil, ctx.Err()
	}
	sessionCtx, cancel := context.WithTimeout(ctx, loginSessionTTL)
	now := time.Now()
	claim, _ := ctx.Value(maintenanceClaimKey{}).(maintenanceClaim)
	sess := &LoginSession{ID: id, Region: region, Status: LoginPending, CreatedAt: now, UpdatedAt: now,
		ctx: sessionCtx, cancel: cancel, release: release, busy: true, claim: claim}
	m.sessions[id], m.activeID = sess, id
	sess.stop = context.AfterFunc(sessionCtx, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.expireLocked(sess)
	})
	m.mu.Unlock()
	// Register cleanup before invoking any transport. A panic must not leave
	// this session counted as active or echo an arbitrary panic value.
	defer func() {
		if recover() != nil {
			out, resultErr = m.failInternal(sess), ErrLoginInternal
		}
	}()

	state, url, err := m.up.StartLoginContext(sessionCtx, region)
	m.mu.Lock()
	defer m.mu.Unlock()
	sess.busy = false
	m.expireLocked(sess)
	if sess.Status != LoginPending {
		m.releaseLocked(sess)
		return m.snapshotLocked(sess), nil
	}
	if err != nil {
		sess.Status, sess.Message = LoginError, "申请授权失败: "+err.Error()
		m.releaseLocked(sess)
		return m.snapshotLocked(sess), err
	}
	sess.State, sess.AuthURL = state, url
	sess.Message = "请在浏览器完成登录后继续"
	sess.UpdatedAt = time.Now()
	return m.snapshotLocked(sess), nil
}

// Poll remains available for the ordinary manager; Service supplies the save
// callback so concurrent polls cover both transport and final credential IO.
func (m *LoginManager) Poll(id string) (*LoginSession, error) {
	if m.desktop {
		return nil, authstore.ErrMaintenanceRequired
	}
	return m.poll(context.Background(), id, nil)
}

func (m *LoginManager) poll(ctx context.Context, id string, save func(context.Context, *authstore.Account, bool) (string, error)) (out *LoginSession, resultErr error) {
	m.mu.Lock()
	sess, err := m.sessionLocked(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.expireLocked(sess)
	if sess.busy {
		m.mu.Unlock()
		return nil, ErrLoginBusy
	}
	if sess.Status != LoginPending && !(sess.Status == LoginSuccess && !sess.Saved) {
		out := m.snapshotLocked(sess)
		m.mu.Unlock()
		return out, nil
	}
	sess.busy = true
	acct, state, region, operationCtx := sess.acct, sess.State, sess.Region, sess.ctx
	m.mu.Unlock()
	defer func() {
		if recover() != nil {
			out, resultErr = m.failInternal(sess), ErrLoginInternal
		}
	}()
	if acct == nil {
		acct, err = m.up.PollLoginContext(operationCtx, region, state)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() { sess.busy = false; m.releaseLocked(sess) }()
	m.expireLocked(sess)
	if sess.Status != LoginPending && !(sess.Status == LoginSuccess && !sess.Saved) {
		return m.snapshotLocked(sess), nil
	}
	if err != nil {
		sess.Status, sess.Message = LoginError, err.Error()
		sess.acct = nil
		return m.snapshotLocked(sess), nil
	}
	if acct == nil {
		sess.Message = "等待授权完成…"
		return m.snapshotLocked(sess), nil
	}
	sess.acct, sess.UID, sess.Nickname = acct, acct.UID, acct.Nickname
	sess.Status, sess.Message = LoginSuccess, "登录成功，正在保存凭证…"
	if save != nil {
		file, err := save(operationCtx, acct, sess.overwriteUID == acct.UID)
		if err != nil {
			sess.credentialErr = err
			sess.Saved = false
			sess.RequiresOverwriteConfirmation = errors.Is(err, ErrOverwriteConfirmationRequired)
			sess.Message = "凭证保存失败: " + err.Error()
			m.expireLocked(sess)
		} else {
			sess.credentialErr = nil
			sess.Saved, sess.File, sess.Message = true, file, "凭证已保存"
			sess.RequiresOverwriteConfirmation = false
			sess.acct, sess.State = nil, ""
		}
	}
	sess.UpdatedAt = time.Now()
	return m.snapshotLocked(sess), nil
}

func (m *LoginManager) failInternal(sess *LoginSession) *LoginSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess.busy = false
	sess.Status, sess.Message = LoginError, ErrLoginInternal.Error()
	sess.acct, sess.State, sess.AuthURL, sess.overwriteUID = nil, "", "", ""
	sess.RequiresOverwriteConfirmation = false
	sess.credentialErr = nil
	sess.UpdatedAt = time.Now()
	m.releaseLocked(sess)
	return m.snapshotLocked(sess)
}

func (m *LoginManager) confirmOverwrite(ctx context.Context, id, uid string) (*LoginSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, err := m.sessionLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	m.expireLocked(sess)
	if sess.busy {
		return nil, ErrLoginBusy
	}
	if sess.Status != LoginSuccess || sess.Saved || !sess.RequiresOverwriteConfirmation || sess.acct == nil || !authstore.ValidUID(uid) || sess.acct.UID != uid {
		return nil, ErrLoginOverwriteNotPending
	}
	sess.overwriteUID = uid
	sess.RequiresOverwriteConfirmation = false
	sess.Message = "已确认覆盖该账号，请继续保存"
	return m.snapshotLocked(sess), nil
}

func (m *LoginManager) sessionLocked(ctx context.Context, id string) (*LoginSession, error) {
	sess, ok := m.sessions[id]
	if !ok {
		return nil, ErrLoginNotFound
	}
	if m.desktop {
		claim, ok := ctx.Value(maintenanceClaimKey{}).(maintenanceClaim)
		if !ok || claim != sess.claim || ctx.Err() != nil {
			return nil, authstore.ErrMaintenanceRequired
		}
	}
	return sess, nil
}

func (m *LoginManager) expireLocked(sess *LoginSession) {
	if sess.Saved || sess.Status == LoginError || sess.Status == LoginCancelled || sess.Status == LoginExpired {
		return
	}
	expired := time.Since(sess.CreatedAt) >= loginSessionTTL
	if deadline, ok := sess.ctx.Deadline(); ok && !time.Now().Before(deadline) {
		expired = true
	}
	if sess.ctx.Err() != nil || expired {
		sess.Status, sess.Message = LoginCancelled, "登录已取消"
		if expired {
			sess.Status, sess.Message = LoginExpired, "登录会话已超时，请重新发起登录"
		}
		sess.acct, sess.State = nil, ""
		sess.AuthURL = ""
		sess.overwriteUID, sess.RequiresOverwriteConfirmation = "", false
		sess.cancel()
		m.releaseLocked(sess)
	}
}

func (m *LoginManager) releaseLocked(sess *LoginSession) {
	terminal := sess.Saved || sess.Status == LoginError || sess.Status == LoginCancelled || sess.Status == LoginExpired
	if sess.busy || !terminal {
		return
	}
	sess.acct, sess.State = nil, ""
	sess.credentialErr = nil
	sess.overwriteUID, sess.RequiresOverwriteConfirmation = "", false
	if sess.stop != nil {
		sess.stop()
		sess.stop = nil
	}
	if sess.cancel != nil {
		sess.cancel()
	}
	if sess.release != nil {
		sess.release()
		sess.release = nil
	}
	if m.activeID == sess.ID {
		m.activeID = ""
	}
}

func (m *LoginManager) Get(id string) (*LoginSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[id]
	if !ok {
		return nil, ErrLoginNotFound
	}
	m.expireLocked(sess)
	return m.snapshotLocked(sess), nil
}

func (m *LoginManager) Cancel(id string) (*LoginSession, error) {
	if m.desktop {
		return nil, authstore.ErrMaintenanceRequired
	}
	return m.cancel(context.Background(), id)
}

func (m *LoginManager) cancel(ctx context.Context, id string) (*LoginSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, err := m.sessionLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	if !sess.Saved && (sess.Status == LoginPending || sess.Status == LoginSuccess) {
		sess.Status, sess.Message = LoginCancelled, "已取消登录"
		sess.acct, sess.State = nil, ""
		sess.AuthURL = ""
		sess.overwriteUID, sess.RequiresOverwriteConfirmation = "", false
		sess.cancel()
		sess.UpdatedAt = time.Now()
	}
	m.releaseLocked(sess)
	return m.snapshotLocked(sess), nil
}

func (m *LoginManager) Current() *LoginSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[m.activeID]
	if sess == nil {
		return nil
	}
	m.expireLocked(sess)
	if m.activeID == "" {
		return nil
	}
	return m.snapshotLocked(sess)
}

func (m *LoginManager) gcLocked(now time.Time) {
	for id, sess := range m.sessions {
		m.expireLocked(sess)
		if !sess.busy && sess.release == nil && now.Sub(sess.UpdatedAt) > loginSessionTTL {
			delete(m.sessions, id)
		}
	}
}

func (m *LoginManager) snapshotLocked(sess *LoginSession) *LoginSession {
	cp := *sess
	cp.acct, cp.ctx, cp.cancel, cp.release, cp.stop = nil, nil, nil, nil, nil
	return &cp
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成会话 id 失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}
