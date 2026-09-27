// loginflow.go 把「登录会话」与「凭证落盘 + 容器重启」串起来。
// LoginManager 只负责 OAuth 轮询；落盘与重启需要 store / docker 能力，故放在 Service 上。
package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/upstream"
)

// StartLogin is the ordinary Web compatibility wrapper.
func (s *Service) StartLogin(region upstream.Region) (*LoginSession, error) {
	if err := s.rejectDesktopLegacy(); err != nil {
		return nil, err
	}
	return s.StartLoginContext(context.Background(), region)
}

func (s *Service) StartLoginContext(ctx context.Context, region upstream.Region) (*LoginSession, error) {
	region, err := upstream.NormalizeRegion(string(region))
	if err != nil {
		return nil, err
	}
	ctx, done, err := s.beginMaintenance(ctx, authstore.MaintainLogin, nil, true)
	if err != nil {
		return nil, err
	}
	return s.logins.start(ctx, region, done)
}

func (s *Service) PollLogin(ctx context.Context, id string) (*LoginSession, error) {
	justSaved := false
	sess, err := s.logins.poll(ctx, id, func(operationCtx context.Context, acct *authstore.Account, overwrite bool) (string, error) {
		if err := s.ensureWritable(); err != nil {
			return "", err
		}
		if err := s.prepareCredential(acct, overwrite); err != nil {
			return "", err
		}
		if err := s.store.SaveContext(operationCtx, acct); err != nil {
			return "", err
		}
		justSaved = true
		return baseName(acct.FilePath), nil
	})
	if err != nil || !justSaved || s.maintenance != nil {
		return sess, err
	}
	// Preserve the ordinary Web server's optional container restart behavior.
	note := "凭证已保存；自动重启已关闭，请手动重启网关使其加载"
	if s.cfg.RefreshConfigOnLogin && s.cfg.DangerousOps && s.cfg.DockerContainer != "" {
		msg, restartErr := s.RestartContainer(ctx)
		if restartErr != nil {
			note = "凭证已保存，但重启容器失败：" + restartErr.Error()
		} else {
			note = msg + "，新账号已加载"
		}
	}
	s.logins.mu.Lock()
	s.logins.sessions[id].Restart = note
	out := s.logins.snapshotLocked(s.logins.sessions[id])
	s.logins.mu.Unlock()
	return out, nil
}

// Confirmation is scoped to the existing session and its already-fetched UID.
// It performs no network or disk IO; the subsequent Poll rechecks before Save.
func (s *Service) ConfirmLoginOverwrite(ctx context.Context, id, uid string) (*LoginSession, error) {
	return s.logins.confirmOverwrite(ctx, id, uid)
}

func (s *Service) CancelLogin(id string) (*LoginSession, error) {
	if err := s.rejectDesktopLegacy(); err != nil {
		return nil, err
	}
	return s.CancelLoginContext(context.Background(), id)
}

func (s *Service) CancelLoginContext(ctx context.Context, id string) (*LoginSession, error) {
	return s.logins.cancel(ctx, id)
}

// ValidateDesktopMaintenance checks HTTP claims without starting another task.
func (s *Service) ValidateDesktopMaintenance(ctx context.Context, action authstore.MaintenanceAction) error {
	if s.maintenance == nil {
		return authstore.ErrMaintenanceRequired
	}
	_, err := s.maintenance.Validate(ctx, action)
	return err
}

func (s *Service) validateLoginMaintenance(ctx context.Context) (authstore.MaintenancePermit, error) {
	if s.maintenance == nil {
		return authstore.MaintenancePermit{}, nil
	}
	return s.maintenance.Validate(ctx, authstore.MaintainLogin)
}

// LoginStatus uses the explicit request binding, including on terminal sessions.
func (s *Service) LoginStatus(ctx context.Context, id string) (*LoginSession, error) {
	if _, err := s.validateLoginMaintenance(ctx); err != nil {
		return nil, err
	}
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	sess, err := s.logins.sessionLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	s.logins.expireLocked(sess)
	return s.logins.snapshotLocked(sess), nil
}

// DesktopLoginURL is only exposed by the independent native control channel.
func (s *Service) DesktopLoginURL(ctx context.Context, id string) (string, time.Time, error) {
	if s.maintenance == nil {
		return "", time.Time{}, authstore.ErrMaintenanceRequired
	}
	permit, err := s.validateLoginMaintenance(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	sess, err := s.logins.sessionLocked(ctx, id)
	if err != nil {
		return "", time.Time{}, err
	}
	s.logins.expireLocked(sess)
	if sess.Status != LoginPending || sess.busy || sess.ctx.Err() != nil || sess.AuthURL == "" {
		return "", time.Time{}, authstore.ErrMaintenanceRequired
	}
	if err := s.ValidateDesktopMaintenance(ctx, authstore.MaintainLogin); err != nil {
		return "", time.Time{}, err
	}
	expires := sess.CreatedAt.Add(loginSessionTTL)
	if permit.ExpiresAt.Before(expires) {
		expires = permit.ExpiresAt
	}
	return sess.AuthURL, expires, nil
}

// SaveManualAuth 手工导入凭证（粘贴 accessToken/refreshToken 或整个 JSON 文件）。
//
// 用途：账号已通过 login.sh 或插件登录过，只想把凭证塞进 auths/ 而不想重走 OAuth；
// 或从别的机器迁移账号。支持两种输入：
//  1. 完整凭证 JSON（嵌套形/扁平形均可）
//  2. 单独粘贴 accessToken + refreshToken + uid
func (s *Service) SaveManualAuth(input ManualAuthInput) (*OpResult, error) {
	if err := s.rejectDesktopLegacy(); err != nil {
		return nil, err
	}
	return s.SaveManualAuthContext(context.Background(), input)
}

func (s *Service) SaveManualAuthContext(ctx context.Context, input ManualAuthInput) (*OpResult, error) {
	if len(input.RawJSON)+len(input.AccessToken)+len(input.RefreshToken)+len(input.UID)+len(input.Nickname)+len(input.EnterpriseID)+len(input.Domain) > 1<<20 {
		return nil, fmt.Errorf("凭证输入超过 1 MiB")
	}
	if err := s.ensureWritable(); err != nil {
		return nil, err
	}
	var acct *authstore.Account
	if strings.TrimSpace(input.RawJSON) != "" {
		parsed, err := authstore.Parse([]byte(input.RawJSON))
		if err != nil {
			return &OpResult{Action: "import", OK: false, Message: "凭证解析失败: " + err.Error()}, nil
		}
		acct = parsed
	} else {
		if strings.TrimSpace(input.AccessToken) == "" {
			return &OpResult{Action: "import", OK: false, Message: "accessToken 不能为空"}, nil
		}
		acct = &authstore.Account{
			AccessToken:  strings.TrimSpace(input.AccessToken),
			RefreshToken: strings.TrimSpace(input.RefreshToken),
			UID:          strings.TrimSpace(input.UID),
			Nickname:     strings.TrimSpace(input.Nickname),
			EnterpriseID: strings.TrimSpace(input.EnterpriseID),
			Domain:       strings.TrimSpace(input.Domain),
			ExpiresAt:    input.ExpiresAt,
		}
	}
	if acct.UID == "" {
		return &OpResult{Action: "import", OK: false, Message: "缺少 uid：无法确定凭证文件名，请补全 uid 字段"}, nil
	}
	if !authstore.ValidUID(acct.UID) {
		return &OpResult{Action: "import", OK: false,
			Message: fmt.Sprintf("uid 含非法字符（仅允许字母数字 . _ -）：%q", acct.UID)}, nil
	}
	if acct.Domain == "" {
		acct.Domain = "copilot.tencent.com"
	}
	// 过期时间缺失时给一个保守值（1 小时后），触发首次使用前自动刷新。
	if acct.ExpiresAt <= 0 {
		acct.ExpiresAt = 0
	}
	ctx, done, err := s.beginMaintenance(ctx, authstore.MaintainImport, []string{acct.UID}, false)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := s.prepareCredential(acct, input.Overwrite); err != nil {
		return nil, err
	}
	if err := s.store.SaveContext(ctx, acct); err != nil {
		return &OpResult{UID: acct.UID, Action: "import", OK: false, Message: "保存失败: " + err.Error()}, nil
	}
	return &OpResult{
		UID: acct.UID, Action: "import", OK: true,
		Message: fmt.Sprintf("凭证已保存到 %s；重启网关后生效", baseName(acct.FilePath)),
		Data:    map[string]any{"file": baseName(acct.FilePath)},
	}, nil
}

// ManualAuthInput 手工导入凭证的输入。
type ManualAuthInput struct {
	RawJSON      string `json:"raw_json"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UID          string `json:"uid"`
	Nickname     string `json:"nickname"`
	EnterpriseID string `json:"enterprise_id"`
	Domain       string `json:"domain"`
	ExpiresAt    int64  `json:"expires_at"`
	Overwrite    bool   `json:"overwrite"`
}

// Desktop imports must preserve the existing user-facing metadata and filename.
// Ambiguous aliases are rejected by Store.Get rather than choosing one silently.
var ErrCredentialRegionConflict = errors.New("同 UID 账号区域不一致，不能覆盖")
var ErrOverwriteConfirmationRequired = errors.New("账号已存在，需要显式确认覆盖")

func credentialRegion(domain string) (upstream.Region, error) {
	switch domain {
	case "", "copilot.tencent.com", "codebuddy.cn", "www.codebuddy.cn":
		return upstream.RegionCN, nil
	case "www.workbuddy.ai", "workbuddy.ai":
		return upstream.RegionGlobal, nil
	default:
		return "", ErrCredentialDomainUnsupported
	}
}

var ErrCredentialDomainUnsupported = errors.New("未知凭证区域 domain")

func (s *Service) prepareCredential(a *authstore.Account, overwrite bool) error {
	if !authstore.ValidUID(a.UID) {
		return fmt.Errorf("非法账号 UID")
	}
	if a.Domain == "" {
		a.Domain = "copilot.tencent.com"
	}
	region, err := credentialRegion(a.Domain)
	if err != nil {
		return err
	}
	old, err := s.store.Get(a.UID)
	if err == nil {
		oldRegion, regionErr := credentialRegion(old.Domain)
		if regionErr != nil || oldRegion != region {
			return ErrCredentialRegionConflict
		}
		if s.maintenance != nil && !overwrite {
			return ErrOverwriteConfirmationRequired
		}
		a.FilePath = old.FilePath
		if s.maintenance != nil {
			a.Nickname = old.Nickname
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
