package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/ops"
)

// ConfigureDesktopMaintenance must run before Handler is served. Even a legacy
// parent without a control key gets the closed credential gate and local reads.
func (s *Server) ConfigureDesktopMaintenance(instance, key string) error {
	if !s.desktop || s.svc == nil || s.desktopInstance != "" {
		return errors.New("invalid desktop maintenance configuration")
	}
	if key != "" {
		raw, err := hex.DecodeString(key)
		if err != nil || len(raw) != 32 || key == s.cfg.UI.Password || key == s.cfg.GatewayAPIKey {
			return errors.New("invalid independent panel control key")
		}
	}
	if err := s.svc.EnableDesktopMaintenance(instance); err != nil {
		return err
	}
	s.desktopInstance, s.desktopControlKey = instance, key
	return nil
}

func (s *Server) desktopControlAuth(w http.ResponseWriter, r *http.Request) bool {
	key := r.Header.Values("X-WorkBuddy-Control-Key")
	if s.desktopControlKey == "" || len(key) != 1 || subtle.ConstantTimeCompare([]byte(key[0]), []byte(s.desktopControlKey)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "CONTROL_UNAUTHORIZED", "error": "原生控制认证失败"})
		return false
	}
	if len(r.Header.Values("Origin")) != 1 || !desktopOrigin(r.Header.Get("Origin"), r.Host) {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "csrf", "error": "跨站请求被拒绝"})
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func (s *Server) maintenanceRequestContext(r *http.Request) (context.Context, bool) {
	instances, generations := r.Header.Values("X-WorkBuddy-Panel-Instance"), r.Header.Values("X-WorkBuddy-Maintenance-Generation")
	if len(instances) != 1 || instances[0] != s.desktopInstance || len(generations) != 1 {
		return nil, false
	}
	generation, err := strconv.ParseUint(generations[0], 10, 64)
	if err != nil || generation == 0 || strconv.FormatUint(generation, 10) != generations[0] {
		return nil, false
	}
	return s.svc.MaintenanceContext(r.Context(), instances[0], generation), true
}

// Only these exact business routes may enter the maintenance context.
func desktopMaintenanceAction(r *http.Request) authstore.MaintenanceAction {
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/api/accounts/import":
			return authstore.MaintainImport
		case "/api/tasks/refresh":
			return authstore.MaintainRefresh
		case "/api/tasks/credits":
			return authstore.MaintainCredits
		case "/api/login/start":
			return authstore.MaintainLogin
		}
		if len(p) == 4 && p[0] == "api" && p[1] == "accounts" {
			if p[3] == "refresh" {
				return authstore.MaintainRefresh
			}
			if p[3] == "credits" {
				return authstore.MaintainCredits
			}
		}
		if len(p) == 4 && p[0] == "api" && p[1] == "login" && (p[3] == "poll" || p[3] == "cancel" || p[3] == "confirm-overwrite") {
			return authstore.MaintainLogin
		}
	}
	if len(p) == 3 && p[0] == "api" {
		if p[1] == "accounts" && r.Method == http.MethodDelete {
			return authstore.MaintainDelete
		}
		if p[1] == "login" && r.Method == http.MethodGet {
			return authstore.MaintainLogin
		}
	}
	return ""
}

func decodeDesktopJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	raw = bytes.TrimSpace(raw)
	if err == nil && len(raw) > 0 && raw[0] == '{' && len(r.Trailer) == 0 {
		fields := json.NewDecoder(bytes.NewReader(raw))
		_, err = fields.Token()
		seen := map[string]bool{}
		for err == nil && fields.More() {
			var key any
			key, err = fields.Token()
			name, ok := key.(string)
			if !ok || name != strings.ToLower(name) || seen[name] {
				err = errors.New("duplicate field")
				break
			}
			seen[name] = true
			var value json.RawMessage
			err = fields.Decode(&value)
		}
		if err != nil {
			invalidDesktopOperation(w)
			return false
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(dst)
		if err == nil && decoder.Decode(new(any)) == io.EOF {
			return true
		}
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_REQUEST", "error": "请求格式错误"})
	return false
}

func (s *Server) handleDesktopMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operation  string          `json:"operation"`
		Instance   string          `json:"instance_id"`
		Generation uint64          `json:"generation"`
		Action     json.RawMessage `json:"action"`
		UIDs       json.RawMessage `json:"uids"`
		ExpiresAt  json.RawMessage `json:"expires_at"`
	}
	if !decodeDesktopJSON(w, r, &req, 1<<16) {
		return
	}
	if req.Instance != s.desktopInstance || req.Generation == 0 {
		s.writeAccountError(w, authstore.ErrMaintenanceRequired)
		return
	}
	var err error
	switch req.Operation {
	case "begin":
		var action authstore.MaintenanceAction
		var uids []string
		var expires time.Time
		if json.Unmarshal(req.Action, &action) != nil || json.Unmarshal(req.UIDs, &uids) != nil || uids == nil ||
			json.Unmarshal(req.ExpiresAt, &expires) != nil || expires.IsZero() {
			invalidDesktopOperation(w)
			return
		}
		err = s.svc.GrantMaintenance(authstore.MaintenancePermit{InstanceID: req.Instance, Generation: req.Generation, Action: action, UIDs: uids, ExpiresAt: expires})
	case "end":
		if req.Action != nil || req.UIDs != nil || req.ExpiresAt != nil {
			invalidDesktopOperation(w)
			return
		}
		err = s.svc.RevokeMaintenance(r.Context(), req.Instance, req.Generation)
	default:
		invalidDesktopOperation(w)
		return
	}
	if err != nil {
		s.writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "drained": req.Operation == "end", "instance_id": req.Instance, "generation": req.Generation})
}

func invalidDesktopOperation(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_REQUEST", "error": "请求格式错误"})
}

func (s *Server) handleDesktopLoginURL(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.maintenanceRequestContext(r)
	if !ok {
		s.writeAccountError(w, authstore.ErrMaintenanceRequired)
		return
	}
	url, expires, err := s.svc.DesktopLoginURL(ctx, r.PathValue("id"))
	if err != nil {
		s.writeLoginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": r.PathValue("id"), "auth_url": url, "expires_at": expires})
}

// Public desktop responses have an explicit non-secret schema. Ordinary Web
// keeps its auth URL and original response contract.
func (s *Server) writeLoginSession(w http.ResponseWriter, sess *ops.LoginSession) {
	if !s.desktop {
		writeJSON(w, http.StatusOK, sess)
		return
	}
	if errors.Is(sess.CredentialError(), ops.ErrCredentialRegionConflict) {
		s.writeLoginError(w, sess.CredentialError())
		return
	}
	message := "等待授权完成"
	switch sess.Status {
	case ops.LoginSuccess:
		message = "授权完成，凭证尚未保存"
		if sess.Saved {
			message = "凭证已保存"
		}
		if sess.RequiresOverwriteConfirmation {
			message = "账号已存在，需要确认覆盖"
		}
	case ops.LoginError:
		message = "登录操作失败，请重新发起"
	case ops.LoginExpired:
		message = "登录会话已过期"
	case ops.LoginCancelled:
		message = "登录已取消"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": sess.ID, "region": sess.Region, "status": sess.Status, "message": message,
		"created_at": sess.CreatedAt, "updated_at": sess.UpdatedAt, "uid": sess.UID,
		"nickname": sess.Nickname, "saved": sess.Saved,
		"requires_overwrite_confirmation": sess.RequiresOverwriteConfirmation,
		"save_error_code":                 loginSaveErrorCode(sess.CredentialError()),
	})
}

func loginSaveErrorCode(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ops.ErrOverwriteConfirmationRequired):
		return "OVERWRITE_CONFIRMATION_REQUIRED"
	case errors.Is(err, ops.ErrCredentialDomainUnsupported):
		return "CREDENTIAL_DOMAIN_UNSUPPORTED"
	case errors.Is(err, authstore.ErrCredentialConflict):
		return "CREDENTIAL_CONFLICT"
	case errors.Is(err, authstore.ErrMaintenanceRequired):
		return "MAINTENANCE_REQUIRED"
	default:
		return "CREDENTIAL_SAVE_FAILED"
	}
}

func stableMaintenanceError(w http.ResponseWriter, err error) bool {
	status, code, message := http.StatusConflict, "", ""
	switch {
	case errors.Is(err, authstore.ErrMaintenanceRequired):
		code, message = "MAINTENANCE_REQUIRED", "需要有效的账号维护许可"
	case errors.Is(err, ops.ErrLoginBusy):
		code, message = "LOGIN_BUSY", "已有登录操作正在进行"
	case errors.Is(err, ops.ErrLoginOverwriteNotPending):
		code, message = "LOGIN_OVERWRITE_NOT_PENDING", "登录会话没有待确认的匹配账号"
	case errors.Is(err, ops.ErrCredentialRegionConflict):
		code, message = "CREDENTIAL_REGION_CONFLICT", "同 UID 账号区域不一致，不能覆盖"
	case errors.Is(err, ops.ErrOverwriteConfirmationRequired):
		code, message = "OVERWRITE_CONFIRMATION_REQUIRED", "账号已存在，需要显式确认覆盖"
	case errors.Is(err, ops.ErrLoginNotFound):
		status, code, message = http.StatusNotFound, "LOGIN_NOT_FOUND", "登录会话不存在或已过期"
	case errors.Is(err, ops.ErrLoginInternal):
		status, code, message = http.StatusInternalServerError, "LOGIN_INTERNAL", "登录操作内部错误"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "MAINTENANCE_DRAIN_INCOMPLETE", "维护工作尚未确认结束"
	default:
		return false
	}
	writeJSON(w, status, map[string]any{"code": code, "error": message})
	return true
}

func (s *Server) writeAccountError(w http.ResponseWriter, err error) {
	if stableMaintenanceError(w, err) {
		return
	}
	if !s.desktop {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "ACCOUNT_OPERATION_FAILED", "error": "账号操作失败"})
}

func (s *Server) writeLoginError(w http.ResponseWriter, err error) {
	if stableMaintenanceError(w, err) {
		return
	}
	if !s.desktop {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "LOGIN_INTERNAL", "error": "登录操作内部错误"})
}
