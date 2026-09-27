package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"workbuddy2api-gui/internal/ops"
)

func (s *Server) handleDesktopConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operation       string          `json:"operation"`
		Instance        string          `json:"instance_id"`
		Revision        json.RawMessage `json:"revision"`
		Patch           json.RawMessage `json:"patch"`
		RotateKey       json.RawMessage `json:"rotate_key"`
		BackupID        json.RawMessage `json:"backup_id"`
		CurrentRevision json.RawMessage `json:"current_revision"`
	}
	if !decodeDesktopJSON(w, r, &req, 32<<10) {
		return
	}
	if req.Instance != s.desktopInstance || req.Instance == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "INSTANCE_MISMATCH", "error": "面板实例不匹配"})
		return
	}
	var revision, backupID string
	var view ops.DesktopConfigView
	var err error
	switch req.Operation {
	case "prepare":
		if !desktopConfigString(req.Revision, &revision) || req.BackupID != nil || req.CurrentRevision != nil ||
			(req.Patch == nil) == (req.RotateKey == nil) || req.RotateKey != nil && !bytes.Equal(bytes.TrimSpace(req.RotateKey), []byte("true")) {
			invalidDesktopOperation(w)
			return
		}
		prepared, prepareErr := s.svc.PrepareDesktopConfig(r.Context(), revision, req.Patch, req.RotateKey != nil)
		if prepareErr != nil {
			writeDesktopConfigError(w, prepareErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backup_id": prepared.BackupID, "revision": prepared.Revision, "candidate_revision": prepared.CandidateRevision})
		return
	case "commit", "restore", "finish":
		if !desktopConfigString(req.BackupID, &backupID) || req.Patch != nil || req.RotateKey != nil || req.Revision != nil ||
			req.Operation != "restore" && req.CurrentRevision != nil {
			invalidDesktopOperation(w)
			return
		}
		if req.Operation == "commit" {
			view, err = s.svc.CommitDesktopConfig(r.Context(), backupID)
		} else if req.Operation == "restore" {
			if !desktopConfigString(req.CurrentRevision, &revision) {
				invalidDesktopOperation(w)
				return
			}
			view, err = s.svc.RestoreDesktopConfig(r.Context(), backupID, revision)
		} else {
			err = s.svc.FinishDesktopConfig(backupID)
		}
	default:
		invalidDesktopOperation(w)
		return
	}
	if err != nil {
		writeDesktopConfigError(w, err)
		return
	}
	if req.Operation == "finish" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": view})
}

func desktopConfigString(raw json.RawMessage, dst *string) bool {
	return raw != nil && json.Unmarshal(raw, dst) == nil && *dst != ""
}

func writeDesktopConfigError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "CONFIG_STORAGE", "配置存储不可用"
	switch {
	case errors.Is(err, ops.ErrDesktopConfigInvalid):
		status, code, message = http.StatusBadRequest, "CONFIG_INVALID", "配置字段或受保护路径无效"
	case errors.Is(err, ops.ErrDesktopConfigConflict):
		status, code, message = http.StatusConflict, "CONFIG_CONFLICT", "配置已变化，请重新读取"
	case errors.Is(err, ops.ErrDesktopConfigBusy):
		status, code, message = http.StatusConflict, "CONFIG_BUSY", "配置事务或账号维护尚未结束"
	case errors.Is(err, ops.ErrDesktopConfigTransaction):
		status, code, message = http.StatusConflict, "CONFIG_TRANSACTION", "配置事务不存在或状态不匹配"
	case errors.Is(err, ops.ErrDesktopConfigBudget):
		status, code, message = http.StatusConflict, "CONFIG_BACKUP_BUDGET", "配置备份容量不足"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "CONFIG_CANCELLED", "配置操作已取消，请核对当前修订号"
	}
	writeJSON(w, status, map[string]any{"code": code, "error": message})
}
