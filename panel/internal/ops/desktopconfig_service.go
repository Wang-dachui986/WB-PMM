package ops

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

var ErrDesktopConfigBusy = errors.New("desktop configuration or account maintenance is active")

type desktopConnection struct {
	url, key, proxy string
	timeout         time.Duration
}

type desktopConfigApply struct {
	prepared  PreparedDesktopConfig
	old, next desktopConnection
	committed bool
}

// EnableDesktopConfig is startup-only. The store and the account directory both
// belong to the explicit native data directory, never a discovered installation.
func (s *Service) EnableDesktopConfig() error {
	if s.maintenance == nil || s.desktopConfig != nil || s.gw == nil || s.up == nil ||
		s.cfg.UpstreamAuthDir != filepath.Join(filepath.Dir(s.cfg.UpstreamConfigFile), "auths") {
		return ErrDesktopConfigStorage
	}
	store, err := NewDesktopConfigStore(s.cfg.UpstreamConfigFile, s.cfg.BackupDir)
	if err != nil {
		return err
	}
	raw, _, err := store.readCurrent()
	if err != nil {
		return err
	}
	connection, err := desktopConnectionFrom(raw)
	if err != nil {
		return err
	}
	if err := s.applyDesktopConnection(connection); err != nil {
		return err
	}
	s.desktopConfig = store
	return nil
}

func (s *Service) DesktopConfigEnabled() bool { return s.desktopConfig != nil }

func (s *Service) ReadDesktopConfig(ctx context.Context) (DesktopConfigView, error) {
	if s.desktopConfig == nil {
		return DesktopConfigView{}, ErrDesktopConfigStorage
	}
	return s.desktopConfig.Read(ctx)
}

func desktopConnectionFrom(raw []byte) (desktopConnection, error) {
	view, err := desktopConfigProjection(raw)
	if err != nil {
		return desktopConnection{}, err
	}
	doc, err := configObject(raw)
	if err != nil {
		return desktopConnection{}, err
	}
	var key string
	if err := configValue(doc, "api_key", &key); err != nil {
		return desktopConnection{}, err
	}
	return desktopConnection{url: fmt.Sprintf("http://127.0.0.1:%d", view.Editable.Port), key: key,
		proxy: view.Editable.Desktop.ProxyURL, timeout: time.Duration(view.Editable.Upstream.TimeoutSeconds) * time.Second}, nil
}

func (s *Service) applyDesktopConnection(connection desktopConnection) error {
	if err := s.up.SetDesktopTransport(connection.proxy, connection.timeout); err != nil {
		return ErrDesktopConfigInvalid
	}
	s.gw.SetConnection(connection.url, connection.key)
	return nil
}

// PrepareDesktopConfig holds the same admission lock as GrantMaintenance. A
// live permit (even with zero workers) or a draining worker prevents preparation.
func (s *Service) PrepareDesktopConfig(ctx context.Context, revision string, patch json.RawMessage, rotateKey bool) (PreparedDesktopConfig, error) {
	s.desktopControlMu.Lock()
	defer s.desktopControlMu.Unlock()
	if s.desktopConfig == nil || s.maintenance == nil {
		return PreparedDesktopConfig{}, ErrDesktopConfigStorage
	}
	state, active, _ := s.maintenance.Status()
	if s.desktopConfigTxn != nil || state != "closed" || active != 0 {
		return PreparedDesktopConfig{}, ErrDesktopConfigBusy
	}
	s.desktopConfig.mu.Lock()
	raw, view, err := s.desktopConfig.readCurrent()
	s.desktopConfig.mu.Unlock()
	if err != nil {
		return PreparedDesktopConfig{}, err
	}
	if view.Revision != revision {
		return PreparedDesktopConfig{}, ErrDesktopConfigConflict
	}
	old, err := desktopConnectionFrom(raw)
	if err != nil {
		return PreparedDesktopConfig{}, err
	}
	var prepared PreparedDesktopConfig
	if rotateKey {
		if patch != nil {
			return PreparedDesktopConfig{}, ErrDesktopConfigInvalid
		}
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return PreparedDesktopConfig{}, ErrDesktopConfigStorage
		}
		prepared, err = s.desktopConfig.PrepareKeyRotation(ctx, revision, key[:])
	} else {
		prepared, err = s.desktopConfig.Prepare(ctx, revision, patch)
	}
	if err != nil {
		return PreparedDesktopConfig{}, err
	}
	s.desktopConfig.mu.Lock()
	next, err := desktopConnectionFrom(s.desktopConfig.active.candidate)
	s.desktopConfig.mu.Unlock()
	if err != nil {
		_ = s.desktopConfig.Finish(prepared.BackupID)
		return PreparedDesktopConfig{}, err
	}
	s.desktopConfigTxn = &desktopConfigApply{prepared: prepared, old: old, next: next}
	return prepared, nil
}

func (s *Service) CommitDesktopConfig(ctx context.Context, backupID string) (DesktopConfigView, error) {
	s.desktopControlMu.Lock()
	defer s.desktopControlMu.Unlock()
	tx := s.desktopConfigTxn
	if tx == nil || tx.prepared.BackupID != backupID || tx.committed {
		return DesktopConfigView{}, ErrDesktopConfigTransaction
	}
	view, err := s.desktopConfig.Commit(ctx, backupID)
	if err != nil {
		s.desktopConfigTxn = nil // This matching prepared transaction failed before replacement.
		return DesktopConfigView{}, err
	}
	tx.committed = true // Preserve rollback even if publishing the connection fails.
	if err := s.applyDesktopConnection(tx.next); err != nil {
		return DesktopConfigView{}, err
	}
	return view, nil
}

func (s *Service) RestoreDesktopConfig(ctx context.Context, backupID, revision string) (DesktopConfigView, error) {
	s.desktopControlMu.Lock()
	defer s.desktopControlMu.Unlock()
	tx := s.desktopConfigTxn
	if tx == nil || tx.prepared.BackupID != backupID || !tx.committed {
		return DesktopConfigView{}, ErrDesktopConfigTransaction
	}
	view, err := s.desktopConfig.Restore(ctx, backupID, revision)
	if err != nil {
		return DesktopConfigView{}, err
	}
	if err := s.applyDesktopConnection(tx.old); err != nil {
		return DesktopConfigView{}, err
	}
	s.desktopConfigTxn = nil
	return view, nil
}

func (s *Service) FinishDesktopConfig(backupID string) error {
	s.desktopControlMu.Lock()
	defer s.desktopControlMu.Unlock()
	// A lost response may be retried after a newer transaction has started.
	// Acknowledge only the completed ID without touching that newer transaction.
	if backupID != "" && backupID == s.lastFinishedBackupID {
		return nil
	}
	if s.desktopConfigTxn == nil || s.desktopConfigTxn.prepared.BackupID != backupID {
		return ErrDesktopConfigTransaction
	}
	if err := s.desktopConfig.Finish(backupID); err != nil {
		return err
	}
	s.lastFinishedBackupID = backupID
	s.desktopConfigTxn = nil
	return nil
}
