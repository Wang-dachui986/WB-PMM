package ops

import (
	"context"
	"fmt"
	"time"

	"workbuddy2api-gui/internal/authstore"
)

type maintenanceClaim struct {
	instance   string
	generation uint64
}
type maintenanceClaimKey struct{}

// EnableDesktopMaintenance is called once, before exposing any HTTP handlers.
func (s *Service) EnableDesktopMaintenance(instance string) error {
	gate, err := authstore.NewMaintenanceGate(instance)
	if err != nil {
		return err
	}
	if err := s.store.EnableMaintenance(gate); err != nil {
		return err
	}
	s.maintenance = gate
	s.logins.desktop = true
	s.up.PrivateErrors = true
	s.tasks.privateErrors = true
	return nil
}

func (s *Service) GrantMaintenance(p authstore.MaintenancePermit) error {
	s.desktopControlMu.Lock()
	defer s.desktopControlMu.Unlock()
	if s.desktopConfigTxn != nil {
		return authstore.ErrMaintenanceRequired
	}
	if s.maintenance == nil || p.Action == authstore.MaintainRename {
		return authstore.ErrMaintenanceRequired
	}
	return s.maintenance.Grant(p)
}

func (s *Service) RevokeMaintenance(ctx context.Context, instance string, generation uint64) error {
	if s.maintenance == nil {
		return authstore.ErrMaintenanceRequired
	}
	return s.maintenance.Revoke(ctx, instance, generation)
}

func (s *Service) MaintenanceContext(ctx context.Context, instance string, generation uint64) context.Context {
	ctx = authstore.BindMaintenance(ctx, instance, generation)
	return context.WithValue(ctx, maintenanceClaimKey{}, maintenanceClaim{instance, generation})
}

func (s *Service) beginMaintenance(ctx context.Context, action authstore.MaintenanceAction, uids []string, detached bool) (context.Context, func(), error) {
	if err := s.ensureWritable(); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if detached {
		ctx = context.WithoutCancel(ctx)
	}
	if s.maintenance != nil {
		return s.maintenance.Begin(ctx, action, uids)
	}
	return ctx, func() {}, nil
}

func (s *Service) rejectDesktopLegacy() error {
	if s.maintenance != nil {
		return authstore.ErrMaintenanceRequired
	}
	return nil
}

func (s *Service) rejectDesktopGrowth() error {
	if s.maintenance != nil {
		return fmt.Errorf("桌面模式不支持签到或旅行")
	}
	return nil
}

func waitAccount(ctx context.Context, delay time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func (s *Service) localProfile(uid string) (map[string]any, error) {
	a, err := s.store.Get(uid)
	if err != nil {
		return nil, err
	}
	v := &AccountView{UID: a.UID, Nickname: a.Nickname, EnterpriseID: a.EnterpriseID, Domain: a.Domain,
		HasFile: true, FileName: baseName(a.FilePath), ExpiresAt: a.ExpiresAt, Expired: a.Expired(),
		NeedsRefresh: a.NeedsRefresh(10 * time.Minute), RefreshToken: a.RefreshToken != "", Status: "unknown"}
	out := map[string]any{"account": v}
	s.mu.RLock()
	if cached, ok := s.credits[uid]; ok {
		at := cached.at
		v.CreditsAt = &at
		v.CreditsStatus, v.CreditsError = cached.status, cached.err
		if cached.err != "" {
			out["credits_error"] = cached.err
		}
		if cached.credits != nil {
			copy := *cached.credits
			out["credits"] = &copy
			v.LiveCredits = &copy.Remain
		}
	}
	s.mu.RUnlock()
	return out, nil
}
