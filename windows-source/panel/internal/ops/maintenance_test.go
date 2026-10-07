package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/config"
	"workbuddy2api-gui/internal/upstream"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeJSON(data string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":` + data + `}`))}, nil
}

func desktopFixture(t *testing.T, transport fakeTransport, accounts ...*authstore.Account) *Service {
	t.Helper()
	store, err := authstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		if err := store.Save(a); err != nil {
			t.Fatal(err)
		}
	}
	up := upstream.New(time.Second)
	up.HTTP.Transport = transport
	// Deliberately no gateway client: these paths must be fully local.
	svc := New(&config.Config{}, store, nil, up)
	if err := svc.EnableDesktopMaintenance("panel-test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, gen := svc.maintenance.Status()
		if gen == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := svc.RevokeMaintenance(ctx, "panel-test", gen); err != nil {
			t.Error(err)
		}
	})
	return svc
}

func grantFixture(t *testing.T, s *Service, gen uint64, action authstore.MaintenanceAction, uids ...string) context.Context {
	t.Helper()
	if err := s.GrantMaintenance(authstore.MaintenancePermit{InstanceID: "panel-test", Generation: gen, Action: action, UIDs: uids, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	return s.MaintenanceContext(context.Background(), "panel-test", gen)
}

func unchangedFile(t *testing.T, path string, original []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("credential changed: %v", err)
	}
}

func TestDesktopCoreFailsClosedAndProfileIsLocal(t *testing.T) {
	var calls atomic.Int32
	a := &authstore.Account{UID: "account-one", AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh"}
	s := desktopFixture(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("unexpected") }, a)
	original, _ := os.ReadFile(a.FilePath)
	ctx := context.Background()
	if _, err := s.RefreshToken(ctx, a.UID); err == nil {
		t.Error("refresh without permit")
	}
	if _, err := s.CreditsFor(ctx, a.UID); err == nil {
		t.Error("credits without permit")
	}
	if _, err := s.BatchRefresh(ctx, []string{a.UID}); err == nil {
		t.Error("batch without permit")
	}
	if _, err := s.StartLogin(upstream.RegionCN); err == nil {
		t.Error("legacy login admitted")
	}
	if err := s.up.RefreshToken(a); err == nil {
		t.Error("legacy upstream refresh admitted")
	}
	if _, err := s.SaveManualAuth(ManualAuthInput{UID: a.UID, AccessToken: "new"}); err == nil {
		t.Error("legacy import admitted")
	}
	if _, err := s.Checkin(ctx, a.UID); err == nil {
		t.Error("desktop checkin admitted")
	}
	if _, err := s.Travel(ctx, a.UID); err == nil {
		t.Error("desktop travel admitted")
	}
	if _, err := s.BatchCheckin(ctx, []string{a.UID}); err == nil {
		t.Error("desktop batch checkin admitted")
	}
	if _, err := s.BatchTravel(ctx, []string{a.UID}); err == nil {
		t.Error("desktop batch travel admitted")
	}
	permitCtx := grantFixture(t, s, 1, authstore.MaintainRefresh, a.UID)
	for _, readCtx := range []context.Context{ctx, permitCtx} {
		if _, err := s.Profile(readCtx, a.UID, false); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("read/denied calls contacted upstream: %d", calls.Load())
	}
	unchangedFile(t, a.FilePath, original)
}

func TestDesktopLateRefreshDrainsBeforeDelete(t *testing.T) {
	for _, action := range []authstore.MaintenanceAction{authstore.MaintainRefresh, authstore.MaintainCredits} {
		t.Run(string(action), func(t *testing.T) {
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			a := &authstore.Account{UID: "account-one", AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh"}
			s := desktopFixture(t, func(r *http.Request) (*http.Response, error) {
				entered <- r.Context()
				<-release
				return fakeJSON(`{"accessToken":"synthetic-late","expiresIn":3600}`)
			}, a)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			original, _ := os.ReadFile(a.FilePath)
			grantFixture(t, s, 1, action, a.UID)
			httpCtx, cancelHTTP := context.WithCancel(context.Background())
			request := s.MaintenanceContext(httpCtx, "panel-test", 1)
			var task *TaskView
			var err error
			if action == authstore.MaintainRefresh {
				task, err = s.BatchRefresh(request, []string{a.UID})
			} else {
				task, err = s.BatchCredits(request, []string{a.UID})
			}
			if err != nil {
				t.Fatal(err)
			}
			transportCtx := <-entered
			cancelHTTP()
			if transportCtx.Err() != nil {
				t.Fatal("HTTP return cancelled detached task")
			}
			revoked := make(chan error, 1)
			go func() { revoked <- s.RevokeMaintenance(context.Background(), "panel-test", 1) }()
			select {
			case <-transportCtx.Done():
			case <-time.After(time.Second):
				t.Fatal("transport not cancelled")
			}
			select {
			case <-revoked:
				t.Fatal("drain returned before late transport")
			default:
			}
			if state, active, _ := s.maintenance.Status(); state != "draining" || active != 1 {
				t.Fatalf("state=%s active=%d", state, active)
			}
			close(release)
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
			unchangedFile(t, a.FilePath, original)
			if v, ok := s.tasks.Get(task.ID); !ok || v.Running {
				t.Fatal("task missing or still running after drain")
			}
			s.setCreditCache(a.UID, &upstream.Credits{Remain: 7})
			deleteCtx := grantFixture(t, s, 2, authstore.MaintainDelete, a.UID)
			if err := s.DeleteAccount(deleteCtx, a.UID); err != nil {
				t.Fatal(err)
			}
			if _, ok := s.credits[a.UID]; ok {
				t.Fatal("delete left cached account reference")
			}
			if _, err := s.RefreshToken(s.MaintenanceContext(context.Background(), "panel-test", 1), a.UID); err == nil {
				t.Fatal("old generation admitted")
			}
			if _, err := s.store.Get(a.UID); !os.IsNotExist(err) {
				t.Fatalf("deleted account resurrected: %v", err)
			}
		})
	}
}

func TestDesktopBatchRejectsInvalidSelection(t *testing.T) {
	a := &authstore.Account{UID: "account-one", AccessToken: "synthetic"}
	s := desktopFixture(t, func(*http.Request) (*http.Response, error) {
		t.Error("unexpected call")
		return nil, errors.New("disabled")
	}, a)
	ctx := grantFixture(t, s, 1, authstore.MaintainRefresh, a.UID)
	for _, uids := range [][]string{nil, {a.UID, a.UID}, {"../bad"}, make([]string, 21), {"missing-account"}} {
		if _, err := s.BatchRefresh(ctx, uids); err == nil {
			t.Errorf("accepted invalid batch: %v", uids)
		}
	}
	if len(s.tasks.Running()) != 0 {
		t.Fatal("invalid batch registered work")
	}
}

func TestDesktopImportValidationAndExplicitOverwrite(t *testing.T) {
	a := &authstore.Account{UID: "account-one", AccessToken: "synthetic-old", Nickname: "用户显示名"}
	s := desktopFixture(t, func(*http.Request) (*http.Response, error) {
		t.Error("import called upstream")
		return nil, errors.New("disabled")
	}, a)
	ctx := grantFixture(t, s, 1, authstore.MaintainImport, a.UID)
	original, _ := os.ReadFile(a.FilePath)
	for _, in := range []ManualAuthInput{{UID: a.UID, AccessToken: "new"}, {UID: a.UID, AccessToken: "new", Domain: "other.invalid", Overwrite: true}, {UID: a.UID, AccessToken: strings.Repeat("x", (1<<20)+1), Overwrite: true}, {UID: "../bad", AccessToken: "new"}} {
		out, err := s.SaveManualAuthContext(ctx, in)
		if err == nil && out.OK {
			t.Fatal("invalid import succeeded")
		}
		unchangedFile(t, a.FilePath, original)
	}
	out, err := s.SaveManualAuthContext(ctx, ManualAuthInput{UID: a.UID, AccessToken: "synthetic-new", Nickname: "替换昵称", Overwrite: true})
	if err != nil || !out.OK {
		t.Fatalf("explicit overwrite=%v %v", out, err)
	}
	got, err := s.store.Get(a.UID)
	if err != nil || got.AccessToken != "synthetic-new" || got.Nickname != a.Nickname {
		t.Fatalf("overwrite lost display metadata: %v", err)
	}
}

func TestTaskHistoryNeverEvictsRunningWork(t *testing.T) {
	m := NewTaskManager()
	m.maxKeep = 2
	release := make(chan struct{})
	defer close(release)
	active := m.New("held", "held", func(*Task) { <-release })
	for i := 0; i < 6; i++ {
		done := make(chan struct{})
		m.NewWithDone("finished", "finished", func() { close(done) }, func(*Task) {})
		<-done
	}
	if v, ok := m.Get(active.ID); !ok || !v.Running {
		t.Fatal("active task evicted")
	}
	if len(m.Running()) != 1 {
		t.Fatal("running task hidden")
	}
}

func TestLoginCancelRejectsLateProfileAndConcurrentPoll(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var starts, polls atomic.Int32
	s := desktopFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			starts.Add(1)
			return fakeJSON(`{"state":"synthetic-state","authUrl":"https://example.invalid/login"}`)
		case "/v2/plugin/auth/token":
			polls.Add(1)
			return fakeJSON(`{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh"}`)
		case "/v2/plugin/login/account":
			entered <- r.Context()
			<-release
			return fakeJSON(`{"uid":"account-one","nickname":"fixture"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			return nil, errors.New("disabled")
		}
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	ctx := grantFixture(t, s, 1, authstore.MaintainLogin)
	sess, err := s.StartLoginContext(ctx, upstream.RegionCN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartLoginContext(ctx, upstream.RegionCN); err == nil {
		t.Fatal("second login admitted")
	}
	result := make(chan *LoginSession, 1)
	go func() { v, _ := s.PollLogin(ctx, sess.ID); result <- v }()
	profileCtx := <-entered
	if _, err := s.PollLogin(ctx, sess.ID); !errors.Is(err, ErrLoginBusy) {
		t.Fatalf("concurrent poll=%v", err)
	}
	if _, err := s.CancelLoginContext(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-profileCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("profile HTTP did not carry cancel context")
	}
	revoked := make(chan error, 1)
	go func() { revoked <- s.RevokeMaintenance(context.Background(), "panel-test", 1) }()
	select {
	case <-revoked:
		t.Fatal("drained while cancelled poll still running")
	default:
	}
	close(release)
	if v := <-result; v == nil || v.Status != LoginCancelled || v.Saved {
		t.Fatalf("late poll revived login: %+v", v)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(s.store.Dir(), "workbuddy*.json"))
	if len(files) != 0 {
		t.Fatal("late cancelled poll wrote credentials")
	}
	if starts.Load() != 1 || polls.Load() != 1 {
		t.Fatalf("duplicate network: starts=%d polls=%d", starts.Load(), polls.Load())
	}
}

func TestLoginSaveFailureStaysUnsavedAndRetriesSameSession(t *testing.T) {
	var polls atomic.Int32
	s := desktopFixture(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			return fakeJSON(`{"state":"synthetic-state"}`)
		case "/v2/plugin/auth/token":
			polls.Add(1)
			return fakeJSON(`{"accessToken":"synthetic-access"}`)
		default:
			return fakeJSON(`{"uid":"account-one","nickname":"fixture"}`)
		}
	})
	ctx := grantFixture(t, s, 1, authstore.MaintainLogin)
	sess, err := s.StartLoginContext(ctx, upstream.RegionCN)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise an actual SaveContext error while retaining the session's valid
	// pending credential for a bounded retry.
	failed, err := s.logins.poll(ctx, sess.ID, func(opCtx context.Context, a *authstore.Account, overwrite bool) (string, error) {
		invalid := *a
		invalid.AccessToken = ""
		return "", s.store.SaveContext(opCtx, &invalid)
	})
	if err != nil || failed.Saved || failed.Status != LoginSuccess {
		t.Fatalf("Save failure marked saved: %+v %v", failed, err)
	}
	retried, err := s.PollLogin(ctx, sess.ID)
	if err != nil || !retried.Saved {
		t.Fatalf("retry failed: %+v %v", retried, err)
	}
	if polls.Load() != 1 {
		t.Fatal("save retry fetched credentials again")
	}
	if _, err := s.store.Get("account-one"); err != nil {
		t.Fatal(err)
	}
}

func TestLoginReservesSessionBeforeStartNetwork(t *testing.T) {
	up := upstream.New(time.Second)
	entered, release := make(chan context.Context, 1), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	up.HTTP.Transport = fakeTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- r.Context()
		<-release
		return fakeJSON(`{"state":"synthetic-state"}`)
	})
	m := NewLoginManager(up)
	result := make(chan *LoginSession, 1)
	go func() { sess, _ := m.Start(upstream.RegionCN); result <- sess }()
	transportCtx := <-entered
	reserved := m.Current()
	if reserved == nil {
		t.Fatal("network started before reserving session")
	}
	if _, err := m.Start(upstream.RegionCN); !errors.Is(err, ErrLoginBusy) {
		t.Fatalf("second Start=%v", err)
	}
	if _, err := m.Cancel(reserved.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transportCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("Start transport not cancelled")
	}
	close(release)
	if sess := <-result; sess == nil || sess.Status != LoginCancelled || sess.AuthURL != "" {
		t.Fatalf("late Start revived session: %+v", sess)
	}
	if calls.Load() != 1 {
		t.Fatalf("multiple Start HTTP calls: %d", calls.Load())
	}
}

func TestLoginPermitExpiryClearsPendingAndDrains(t *testing.T) {
	var calls atomic.Int32
	s := desktopFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return fakeJSON(`{"state":"synthetic-state"}`)
	})
	expires := time.Now().Add(80 * time.Millisecond)
	if err := s.GrantMaintenance(authstore.MaintenancePermit{InstanceID: "panel-test", Generation: 1, Action: authstore.MaintainLogin, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	ctx := s.MaintenanceContext(context.Background(), "panel-test", 1)
	sess, err := s.StartLoginContext(ctx, upstream.RegionCN)
	if err != nil {
		t.Fatal(err)
	}
	s.logins.mu.Lock()
	opCtx := s.logins.sessions[sess.ID].ctx
	s.logins.mu.Unlock()
	select {
	case <-opCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("permit did not expire")
	}
	// Get performs the same locked expiry check even if cancellation callback
	// scheduling has not run yet.
	expired, err := s.logins.Get(sess.ID)
	if err != nil || expired.Status != LoginExpired || expired.Saved || expired.AuthURL != "" {
		t.Fatalf("expired session=%+v %v", expired, err)
	}
	if err := s.RevokeMaintenance(context.Background(), "panel-test", 1); err != nil {
		t.Fatal(err)
	}
	if _, active, _ := s.maintenance.Status(); active != 0 {
		t.Fatal("expired session did not drain")
	}
	if _, err := s.PollLogin(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("expired Poll contacted upstream")
	}
}

func TestDesktopErrorsDoNotExportUpstreamCredentials(t *testing.T) {
	const sentinel = "SYNTHETIC_SECRET_SENTINEL"
	malicious := func() (*http.Response, error) {
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(sentinel + ` accessToken=` + sentinel + ` refreshToken=` + sentinel + ` Authorization=Bearer ` + sentinel + ` authUrl=https://example.invalid/?state=` + sentinel))}, nil
	}
	assertSafe := func(t *testing.T, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), sentinel) {
			t.Fatalf("exported upstream secret: %s", raw)
		}
		if !strings.Contains(string(raw), "500") {
			t.Fatalf("stable HTTP classification missing: %s", raw)
		}
	}
	t.Run("login session", func(t *testing.T) {
		s := desktopFixture(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v2/plugin/auth/state" {
				return fakeJSON(`{"state":"synthetic-state"}`)
			}
			return malicious()
		})
		ctx := grantFixture(t, s, 1, authstore.MaintainLogin)
		sess, err := s.StartLoginContext(ctx, upstream.RegionCN)
		if err != nil {
			t.Fatal(err)
		}
		failed, err := s.PollLogin(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Status != LoginError || failed.Saved {
			t.Fatalf("unexpected login result: %+v", failed)
		}
		assertSafe(t, failed)
	})
	t.Run("task", func(t *testing.T) {
		s := desktopFixture(t, func(*http.Request) (*http.Response, error) { return malicious() }, &authstore.Account{UID: "account-one", AccessToken: "synthetic", RefreshToken: "synthetic-refresh"})
		ctx := grantFixture(t, s, 1, authstore.MaintainRefresh, "account-one")
		task, err := s.BatchRefresh(ctx, []string{"account-one"})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for {
			view, ok := s.tasks.Get(task.ID)
			if !ok {
				t.Fatal("task disappeared")
			}
			if !view.Running {
				if view.Failed != 1 {
					t.Fatal("error task reported success")
				}
				assertSafe(t, view)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("task did not finish")
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func TestImportRejectsCrossRegionEvenWithOverwrite(t *testing.T) {
	for _, domains := range [][2]string{{"copilot.tencent.com", "www.workbuddy.ai"}, {"workbuddy.ai", "copilot.tencent.com"}} {
		t.Run(domains[0], func(t *testing.T) {
			a := &authstore.Account{UID: "account-one", AccessToken: "synthetic-old", Domain: domains[0]}
			s := desktopFixture(t, func(*http.Request) (*http.Response, error) {
				t.Error("import called upstream")
				return nil, errors.New("disabled")
			}, a)
			original, _ := os.ReadFile(a.FilePath)
			ctx := grantFixture(t, s, 1, authstore.MaintainImport, a.UID)
			_, err := s.SaveManualAuthContext(ctx, ManualAuthInput{UID: a.UID, AccessToken: "synthetic-new", Domain: domains[1], Overwrite: true})
			if !errors.Is(err, ErrCredentialRegionConflict) {
				t.Fatalf("cross-region import=%v", err)
			}
			unchangedFile(t, a.FilePath, original)
		})
	}
}

func TestOAuthDuplicateRequiresSessionUIDConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, oldDomain, newDomain string
		cross                      bool
	}{
		{"same CN", "copilot.tencent.com", "copilot.tencent.com", false},
		{"same global aliases", "workbuddy.ai", "www.workbuddy.ai", false},
		{"cross region", "copilot.tencent.com", "www.workbuddy.ai", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var polls atomic.Int32
			a := &authstore.Account{UID: "account-one", AccessToken: "synthetic-old", Domain: tc.oldDomain, Nickname: "本地显示名"}
			s := desktopFixture(t, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v2/plugin/auth/state":
					return fakeJSON(`{"state":"synthetic-state"}`)
				case "/v2/plugin/auth/token":
					polls.Add(1)
					return fakeJSON(`{"accessToken":"synthetic-new","domain":"` + tc.newDomain + `"}`)
				default:
					return fakeJSON(`{"uid":"account-one","nickname":"上游昵称"}`)
				}
			}, a)
			original, _ := os.ReadFile(a.FilePath)
			ctx := grantFixture(t, s, 1, authstore.MaintainLogin)
			region, _ := credentialRegion(tc.newDomain)
			sess, err := s.StartLoginContext(ctx, region)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := s.PollLogin(ctx, sess.ID)
			if err != nil || pending.Saved || pending.Status != LoginSuccess {
				t.Fatalf("duplicate auto-saved: %+v %v", pending, err)
			}
			unchangedFile(t, a.FilePath, original)
			if tc.cross {
				if pending.RequiresOverwriteConfirmation {
					t.Fatal("cross-region offered overwrite")
				}
				if _, err := s.ConfirmLoginOverwrite(ctx, sess.ID, a.UID); !errors.Is(err, ErrLoginOverwriteNotPending) {
					t.Fatalf("cross-region confirmation=%v", err)
				}
				again, err := s.PollLogin(ctx, sess.ID)
				if err != nil || again.Saved {
					t.Fatal("cross-region retry saved")
				}
				unchangedFile(t, a.FilePath, original)
				return
			}
			if !pending.RequiresOverwriteConfirmation {
				t.Fatal("missing explicit confirmation state")
			}
			if _, err := s.ConfirmLoginOverwrite(ctx, sess.ID, "account-other"); !errors.Is(err, ErrLoginOverwriteNotPending) {
				t.Fatalf("wrong UID confirmation=%v", err)
			}
			if _, err := s.ConfirmLoginOverwrite(s.MaintenanceContext(context.Background(), "panel-test", 2), sess.ID, a.UID); !errors.Is(err, authstore.ErrMaintenanceRequired) {
				t.Fatalf("wrong generation confirmation=%v", err)
			}
			if _, err := s.ConfirmLoginOverwrite(ctx, sess.ID, a.UID); err != nil {
				t.Fatal(err)
			}
			unchangedFile(t, a.FilePath, original)
			saved, err := s.PollLogin(ctx, sess.ID)
			if err != nil || !saved.Saved {
				t.Fatalf("confirmed retry=%+v %v", saved, err)
			}
			got, err := s.store.Get(a.UID)
			if err != nil || got.AccessToken != "synthetic-new" || got.Nickname != a.Nickname {
				t.Fatalf("confirmed save failed or lost metadata: %v", err)
			}
			if polls.Load() != 1 {
				t.Fatal("confirmed retry fetched a potentially different account")
			}
		})
	}
}

func TestLoginTransportPanicReleasesPermitWithoutSecret(t *testing.T) {
	const sentinel = "SYNTHETIC_PANIC_ACCESS_REFRESH_SECRET"
	for _, path := range []string{"/v2/plugin/auth/state", "/v2/plugin/auth/token", "/v2/plugin/login/account"} {
		t.Run(path, func(t *testing.T) {
			var panicEnabled atomic.Bool
			panicEnabled.Store(true)
			s := desktopFixture(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == path && panicEnabled.Load() {
					panic(sentinel)
				}
				switch r.URL.Path {
				case "/v2/plugin/auth/state":
					return fakeJSON(`{"state":"synthetic-state"}`)
				case "/v2/plugin/auth/token":
					return fakeJSON(`{"accessToken":"synthetic-access"}`)
				default:
					return fakeJSON(`{"uid":"account-one"}`)
				}
			})
			ctx := grantFixture(t, s, 1, authstore.MaintainLogin)
			sess, err := s.StartLoginContext(ctx, upstream.RegionCN)
			if path != "/v2/plugin/auth/state" {
				if err != nil {
					t.Fatal(err)
				}
				sess, err = s.PollLogin(ctx, sess.ID)
			}
			if !errors.Is(err, ErrLoginInternal) || sess == nil || sess.Status != LoginError || sess.Saved {
				t.Fatalf("panic result=%+v %v", sess, err)
			}
			raw, _ := json.Marshal(sess)
			if strings.Contains(string(raw)+err.Error(), sentinel) {
				t.Fatal("panic value leaked")
			}
			s.logins.mu.Lock()
			internal := s.logins.sessions[sess.ID]
			clean := !internal.busy && internal.acct == nil && internal.State == "" && internal.AuthURL == "" && internal.release == nil
			s.logins.mu.Unlock()
			if !clean {
				t.Fatal("panic left active or sensitive session state")
			}
			if _, active, _ := s.maintenance.Status(); active != 0 {
				t.Fatalf("panic leaked active=%d", active)
			}
			wait, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.RevokeMaintenance(wait, "panel-test", 1); err != nil {
				t.Fatal(err)
			}
			panicEnabled.Store(false)
			next := grantFixture(t, s, 2, authstore.MaintainLogin)
			second, err := s.StartLoginContext(next, upstream.RegionCN)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CancelLoginContext(next, second.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWebBatchGrowthDetachesRequestContext(t *testing.T) {
	for _, kind := range []string{"checkin", "travel"} {
		t.Run(kind, func(t *testing.T) {
			store, err := authstore.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			a := &authstore.Account{UID: "account-one", AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh"}
			if err := store.Save(a); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan context.Context, 1), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			up := upstream.New(time.Second)
			up.HTTP.Transport = fakeTransport(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/v2/plugin/auth/token/refresh":
					entered <- r.Context()
					<-release
					return fakeJSON(`{"accessToken":"synthetic-new","expiresIn":3600}`)
				case upstream.BuddyInfoPath:
					return fakeJSON(`{"buddy":{"id":1,"name":"fixture"}}`)
				case upstream.TravelStatusPath:
					return fakeJSON(`{"state":"traveling"}`)
				default:
					return fakeJSON(`{}`)
				}
			})
			s := New(&config.Config{}, store, nil, up)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var task *TaskView
			if kind == "checkin" {
				task, err = s.BatchCheckin(ctx, []string{a.UID})
			} else {
				task, err = s.BatchTravel(ctx, []string{a.UID})
			}
			if err != nil {
				t.Fatal(err)
			}
			transportCtx := <-entered
			cancel()
			if transportCtx.Err() != nil {
				t.Fatal("Web background task retained HTTP cancellation")
			}
			close(release)
			deadline := time.Now().Add(time.Second)
			for {
				view, ok := s.tasks.Get(task.ID)
				if !ok {
					t.Fatal("task missing")
				}
				if !view.Running {
					if view.OK != 1 || view.Failed != 0 {
						t.Fatalf("Web task failed after HTTP cancellation: %+v", view)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("task did not finish")
				}
				time.Sleep(time.Millisecond)
			}
			got, err := store.Get(a.UID)
			if err != nil || got.AccessToken != "synthetic-new" {
				t.Fatalf("Web task did not save refreshed credential: %v", err)
			}
		})
	}
}

func TestWebOAuthSameRegionOverwriteRemainsCompatible(t *testing.T) {
	store, err := authstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &authstore.Account{UID: "account-one", AccessToken: "synthetic-old", Domain: "copilot.tencent.com"}
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	up := upstream.New(time.Second)
	up.HTTP.Transport = fakeTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			return fakeJSON(`{"state":"synthetic-state"}`)
		case "/v2/plugin/auth/token":
			return fakeJSON(`{"accessToken":"synthetic-new"}`)
		default:
			return fakeJSON(`{"uid":"account-one"}`)
		}
	})
	s := New(&config.Config{}, store, nil, up)
	sess, err := s.StartLogin(upstream.RegionCN)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.PollLogin(context.Background(), sess.ID)
	if err != nil || !saved.Saved {
		t.Fatalf("Web OAuth compatibility=%+v %v", saved, err)
	}
	got, err := store.Get(a.UID)
	if err != nil || got.AccessToken != "synthetic-new" {
		t.Fatal("Web OAuth overwrite failed")
	}
}
