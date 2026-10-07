package authstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrMaintenanceRequired = errors.New("需要有效的账号维护许可")

type MaintenanceAction string

const (
	MaintainLogin   MaintenanceAction = "login"
	MaintainImport  MaintenanceAction = "import"
	MaintainRefresh MaintenanceAction = "refresh"
	MaintainCredits MaintenanceAction = "credits"
	MaintainDelete  MaintenanceAction = "delete"
	MaintainRename  MaintenanceAction = "rename"
)

// MaintenancePermit is issued only by the authenticated native control channel.
type MaintenancePermit struct {
	InstanceID string            `json:"instance_id"`
	Generation uint64            `json:"generation"`
	Action     MaintenanceAction `json:"action"`
	UIDs       []string          `json:"uids"`
	ExpiresAt  time.Time         `json:"expires_at"`
}

type maintenanceBinding struct {
	instance   string
	generation uint64
}

type maintenanceBindingKey struct{}
type maintenanceOperationKey struct{}

// BindMaintenance does not grant permission. Begin still checks these claims
// against the currently installed permit before registering any work.
func BindMaintenance(ctx context.Context, instance string, generation uint64) context.Context {
	return context.WithValue(ctx, maintenanceBindingKey{}, maintenanceBinding{instance, generation})
}

type maintenanceOperation struct {
	gate       *MaintenanceGate
	generation uint64
	action     MaintenanceAction
	uids       []string
	active     bool // guarded by gate.mu
}

// MaintenanceGate serializes revoke and the whole credential write, not just a
// permission check before the write. It must be installed before serving HTTP.
// ponytail: one maintenance flow per panel; split ownership only if concurrent maintenance becomes a requirement.
type MaintenanceGate struct {
	mu       sync.Mutex
	instance string
	state    string // closed | active | draining
	permit   MaintenancePermit
	ctx      context.Context
	cancel   context.CancelFunc
	active   int
	drained  chan struct{}
}

func NewMaintenanceGate(instance string) (*MaintenanceGate, error) {
	if instance == "" || len(instance) > 128 {
		return nil, fmt.Errorf("invalid panel instance")
	}
	done := make(chan struct{})
	close(done)
	return &MaintenanceGate{instance: instance, state: "closed", drained: done}, nil
}

func (g *MaintenanceGate) Grant(p MaintenancePermit) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	remaining := time.Until(p.ExpiresAt)
	if g.state != "closed" || p.InstanceID != g.instance || p.Generation <= g.permit.Generation || remaining <= 0 || remaining > 15*time.Minute {
		return ErrMaintenanceRequired
	}
	switch p.Action {
	case MaintainLogin:
		if len(p.UIDs) > 1 {
			return ErrMaintenanceRequired
		}
	case MaintainImport, MaintainDelete:
		if len(p.UIDs) != 1 {
			return ErrMaintenanceRequired
		}
	case MaintainRefresh, MaintainCredits:
		if len(p.UIDs) < 1 || len(p.UIDs) > 20 {
			return ErrMaintenanceRequired
		}
	case MaintainRename:
		if len(p.UIDs) != 2 {
			return ErrMaintenanceRequired
		}
	default:
		return ErrMaintenanceRequired
	}
	seen := make(map[string]bool, len(p.UIDs))
	for _, uid := range p.UIDs {
		if !ValidUID(uid) || seen[uid] {
			return ErrMaintenanceRequired
		}
		seen[uid] = true
	}
	p.UIDs = append([]string(nil), p.UIDs...)
	g.permit = p
	g.ctx, g.cancel = context.WithDeadline(context.Background(), p.ExpiresAt)
	g.state = "active"
	return nil
}

// Begin registers work before its goroutine starts. done must run after all of
// that work has finished, including a cancelled transport that returns late.
func (g *MaintenanceGate) Begin(ctx context.Context, action MaintenanceAction, uids []string) (context.Context, func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := ctx.Value(maintenanceBindingKey{}).(maintenanceBinding)
	if !ok || b.instance != g.instance || b.generation != g.permit.Generation || g.state != "active" || g.active != 0 || ctx.Err() != nil || g.ctx.Err() != nil || action != g.permit.Action {
		return nil, nil, ErrMaintenanceRequired
	}
	if (len(uids) == 0 && action != MaintainLogin) || len(uids) > 20 {
		return nil, nil, ErrMaintenanceRequired
	}
	seen := make(map[string]bool, len(uids))
	for _, uid := range uids {
		if !g.allowsUIDLocked(uid) || seen[uid] {
			return nil, nil, ErrMaintenanceRequired
		}
		seen[uid] = true
	}
	if g.active == 0 {
		g.drained = make(chan struct{})
	}
	g.active++
	op := &maintenanceOperation{gate: g, generation: b.generation, action: action, uids: append([]string(nil), uids...), active: true}
	opCtx, cancel := context.WithDeadline(ctx, g.permit.ExpiresAt)
	stop := context.AfterFunc(g.ctx, cancel)
	opCtx = context.WithValue(opCtx, maintenanceOperationKey{}, op)
	var once sync.Once
	done := func() {
		once.Do(func() {
			stop()
			cancel()
			g.mu.Lock()
			op.active = false
			g.active--
			if g.active == 0 {
				close(g.drained)
			}
			g.mu.Unlock()
		})
	}
	return opCtx, done, nil
}

func (g *MaintenanceGate) allowsUIDLocked(uid string) bool {
	if !ValidUID(uid) {
		return false
	}
	if g.permit.Action == MaintainLogin && len(g.permit.UIDs) == 0 {
		return true
	}
	for _, allowed := range g.permit.UIDs {
		if allowed == uid {
			return true
		}
	}
	return false
}

// withWrite keeps the generation check and disk mutation ordered with Revoke.
func (g *MaintenanceGate) withWrite(ctx context.Context, kind string, uids []string, write func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	op, ok := ctx.Value(maintenanceOperationKey{}).(*maintenanceOperation)
	if !ok || op.gate != g || !op.active || op.generation != g.permit.Generation || g.state != "active" || ctx.Err() != nil || g.ctx.Err() != nil {
		return ErrMaintenanceRequired
	}
	if (kind == "save" && (op.action == MaintainDelete || op.action == MaintainRename)) ||
		(kind == "delete" && op.action != MaintainDelete) || (kind == "rename" && op.action != MaintainRename) {
		return ErrMaintenanceRequired
	}
	for _, uid := range uids {
		if !g.allowsUIDLocked(uid) {
			return ErrMaintenanceRequired
		}
		selected := len(op.uids) == 0 && op.action == MaintainLogin
		for _, chosen := range op.uids {
			selected = selected || chosen == uid
		}
		if !selected {
			return ErrMaintenanceRequired
		}
	}
	return write()
}

// Revoke closes admission, cancels work and waits until every admitted caller
// has returned. A timed-out revoke stays draining; Grant cannot reopen it.
func (g *MaintenanceGate) Revoke(ctx context.Context, instance string, generation uint64) error {
	g.mu.Lock()
	if instance != g.instance || generation == 0 || generation != g.permit.Generation {
		g.mu.Unlock()
		return ErrMaintenanceRequired
	}
	if g.state == "closed" {
		g.mu.Unlock()
		return nil
	}
	g.state = "draining"
	g.cancel()
	drained := g.drained
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-drained:
		g.mu.Lock()
		if g.permit.Generation == generation && g.state == "draining" && g.active == 0 {
			g.state = "closed"
		}
		g.mu.Unlock()
		return nil
	}
}

func (g *MaintenanceGate) Status() (state string, active int, generation uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state, g.active, g.permit.Generation
}

// Validate checks a request binding without admitting work or extending its lease.
// Existing login work may be active while its owner queries or cancels the session.
func (g *MaintenanceGate) Validate(ctx context.Context, action MaintenanceAction) (MaintenancePermit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := ctx.Value(maintenanceBindingKey{}).(maintenanceBinding)
	if !ok || b.instance != g.instance || b.generation != g.permit.Generation ||
		g.state != "active" || ctx.Err() != nil || g.ctx.Err() != nil ||
		!time.Now().Before(g.permit.ExpiresAt) || action != g.permit.Action {
		return MaintenancePermit{}, ErrMaintenanceRequired
	}
	p := g.permit
	p.UIDs = append([]string(nil), p.UIDs...)
	return p, nil
}
