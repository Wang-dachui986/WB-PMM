package ops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const desktopConfigFixture = `{
  "listen": "127.0.0.1:7863",
  "api_key": "FAKE_CONFIG_KEY_DO_NOT_EXPORT",
  "auth_dir": "/Users/fake-private-user/auths",
  "state_file": "/Users/fake-private-user/state.json",
  "admin": {"enabled":false},
  "schedule": {"checkin_enabled":false,"travel_enabled":false,"activity_enabled":false,"keepalive_enabled":false,"school_enabled":false,"cat_enabled":false},
  "upstream": {"device_token":"FAKE_DEVICE_TOKEN_DO_NOT_EXPORT","custom_integer":123456789012345678901234567890},
  "custom_old_field": {"integer":987654321098765432109876543210,"list":[true,"keep-me"]},
  "upstash": {"token":"FAKE_UPSTASH_TOKEN_DO_NOT_EXPORT"}
}
`

func newDesktopConfigFixture(t *testing.T, raw string) *DesktopConfigStore {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	raw = strings.ReplaceAll(raw, "/Users/fake-private-user", dir)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewDesktopConfigStore(path, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func localDesktopConfig(s *DesktopConfigStore) string {
	return strings.ReplaceAll(desktopConfigFixture, "/Users/fake-private-user", filepath.Dir(s.path))
}

func readDesktopFixture(t *testing.T, s *DesktopConfigStore) DesktopConfigView {
	t.Helper()
	view, err := s.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func mustPrepareDesktop(t *testing.T, s *DesktopConfigStore, patch string) PreparedDesktopConfig {
	t.Helper()
	prepared, err := s.Prepare(context.Background(), readDesktopFixture(t, s).Revision, []byte(patch))
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestDesktopConfigProjectionAndExactRestore(t *testing.T) {
	s := newDesktopConfigFixture(t, desktopConfigFixture)
	before := readDesktopFixture(t, s)
	if before.Revision != configRevision([]byte(localDesktopConfig(s))) || !before.KeyConfigured ||
		before.Editable.Port != 7863 || before.Editable.Upstream.TimeoutSeconds != 120 ||
		before.Editable.Upstream.HeaderTimeoutSeconds != 120 || before.Editable.Upstream.IdleTimeoutSeconds != 300 ||
		before.Editable.Pool.MaxInFlight != 3 || !before.Editable.SessionSticky.Enabled ||
		before.Editable.SessionSticky.TTL != "30m" || before.Editable.Desktop.ProxyURL != "" ||
		before.Editable.Schedule.CheckinEnabled || !slices.Equal(before.Editable.Schedule.CheckinHours, []int{9, 21}) ||
		!before.SafeSummary.SchedulesDisabled {
		t.Fatalf("unexpected safe defaults: %+v", before)
	}
	encoded, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 4 || top["revision"] == nil || top["editable"] == nil || top["safe_summary"] == nil || top["key_configured"] == nil {
		t.Fatal("projection schema changed")
	}
	for _, forbidden := range []string{"FAKE_", filepath.Dir(s.path), "api_key", "device_token", "upstash", "auth_dir", "state_file"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("projection leaked %q", forbidden)
		}
	}
	patch := `{"port":8788,"upstream":{"timeout_seconds":45,"header_timeout_seconds":60,"idle_timeout_seconds":600},"pool":{"max_in_flight":0},"session_sticky":{"enabled":false,"ttl":"2h"},"desktop":{"proxy_url":"http://127.0.0.1:9000"}}`
	tx := mustPrepareDesktop(t, s, patch)
	if tx.Revision != before.Revision || tx.CandidateRevision == before.Revision || len(tx.BackupID) != 32 {
		t.Fatalf("invalid prepare metadata: %+v", tx)
	}
	backup, err := os.ReadFile(s.backupPath(tx.BackupID))
	if err != nil || string(backup) != localDesktopConfig(s) {
		t.Fatalf("backup was not byte exact: %v", err)
	}
	if got, _ := os.ReadFile(s.path); string(got) != localDesktopConfig(s) {
		t.Fatal("prepare changed current config")
	}
	after, err := s.Commit(context.Background(), tx.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != tx.CandidateRevision || after.Editable.Port != 8788 || after.Editable.Pool.MaxInFlight != 0 || after.Editable.SessionSticky.Enabled || after.Editable.Desktop.ProxyURL != "http://127.0.0.1:9000" {
		t.Fatalf("wrong committed projection: %+v", after)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var upstream map[string]json.RawMessage
	if err := json.Unmarshal(doc["upstream"], &upstream); err != nil {
		t.Fatal(err)
	}
	if string(upstream["custom_integer"]) != "123456789012345678901234567890" || !bytes.Contains(doc["custom_old_field"], []byte("987654321098765432109876543210")) || string(doc["api_key"]) != `"FAKE_CONFIG_KEY_DO_NOT_EXPORT"` || string(upstream["device_token"]) != `"FAKE_DEVICE_TOKEN_DO_NOT_EXPORT"` {
		t.Fatal("patch lost unedited legacy data")
	}
	if _, err := s.Prepare(context.Background(), after.Revision, []byte(`{"port":9001}`)); !errors.Is(err, ErrDesktopConfigConflict) {
		t.Fatalf("pending application was not protected: %v", err)
	}
	restored, err := s.Restore(context.Background(), tx.BackupID, after.Revision)
	if err != nil || restored.Revision != before.Revision {
		t.Fatalf("restore failed: %v", err)
	}
	if got, _ := os.ReadFile(s.path); string(got) != localDesktopConfig(s) {
		t.Fatal("restore did not preserve exact original bytes")
	}
	if s.active != nil {
		t.Fatal("successful restore left active transaction")
	}
	for path, mode := range map[string]os.FileMode{s.path: 0o600, s.backupDir: 0o700, s.backupPath(tx.BackupID): 0o600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("wrong private permissions: %v", err)
		}
	}
}

func TestDesktopConfigCheckinScheduleProjectionAndCommit(t *testing.T) {
	s := newDesktopConfigFixture(t, desktopConfigFixture)
	before := readDesktopFixture(t, s)
	if before.Editable.Schedule.CheckinEnabled || !slices.Equal(before.Editable.Schedule.CheckinHours, []int{9, 21}) {
		t.Fatal("legacy disabled check-in or default hours changed")
	}
	tx := mustPrepareDesktop(t, s, `{"schedule":{"checkin_enabled":true,"checkin_hours":[8,20]}}`)
	after, err := s.Commit(context.Background(), tx.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Editable.Schedule.CheckinEnabled || !slices.Equal(after.Editable.Schedule.CheckinHours, []int{8, 20}) || after.SafeSummary.SchedulesDisabled {
		t.Fatalf("wrong check-in projection: %+v", after.Editable.Schedule)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schedule map[string]json.RawMessage `json:"schedule"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"travel_enabled", "activity_enabled", "keepalive_enabled", "school_enabled", "cat_enabled"} {
		if string(doc.Schedule[name]) != "false" {
			t.Fatalf("%s changed from disabled", name)
		}
	}
	if err := s.Finish(tx.BackupID); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopConfigRejectsInvalidPatchesWithoutBackup(t *testing.T) {
	s := newDesktopConfigFixture(t, desktopConfigFixture)
	revision := readDesktopFixture(t, s).Revision
	patches := []string{
		``, `null`, `[]`, `{}`, `{"port":null}`, `{"port":1023}`, `{"port":65536}`, `{"port":"9000"}`, `{"port":9000.0}`, `{"port":1e4}`, `{"port":1e309}`, `{"port":NaN}`, `{"port":9000,"port":9001}`,
		`{"listen":"127.0.0.1:9000"}`, `{"api_key":"fake"}`, `{"auth_dir":"elsewhere"}`, `{"state_file":"elsewhere"}`, `{"admin":{"enabled":true}}`, `{"schedule":{"cat_enabled":true}}`, `{"unknown":false}`, `{"Port":9000}`,
		`{"schedule":null}`, `{"schedule":{}}`, `{"schedule":{"checkin_enabled":1}}`, `{"schedule":{"checkin_enabled":null}}`,
		`{"schedule":{"checkin_hours":[]}}`, `{"schedule":{"checkin_hours":null}}`, `{"schedule":{"checkin_hours":[-1]}}`, `{"schedule":{"checkin_hours":[24]}}`, `{"schedule":{"checkin_hours":[9,9]}}`, `{"schedule":{"checkin_hours":[true]}}`, `{"schedule":{"checkin_hours":[9.0]}}`, `{"schedule":{"checkin_hours":"9"}}`,
		`{"pool":null}`, `{"pool":{"max_in_flight":null}}`, `{"pool":{"max_in_flight":-1}}`, `{"pool":{"max_in_flight":9999999999999999999999999}}`, `{"pool":{"max_in_flight_global":2}}`,
		`{"upstream":{}}`, `{"upstream":true}`, `{"upstream":{"timeout_seconds":0}}`, `{"upstream":{"header_timeout_seconds":-1}}`, `{"upstream":{"idle_timeout_seconds":86401}}`, `{"upstream":{"timeout_seconds":true}}`, `{"upstream":{"device_token":"fake"}}`,
		`{"session_sticky":{"enabled":1}}`, `{"session_sticky":{"enabled":null}}`, `{"session_sticky":{"ttl":30}}`, `{"session_sticky":{"ttl":"0s"}}`, `{"session_sticky":{"ttl":"-1h"}}`, `{"session_sticky":{"ttl":"721h"}}`, `{"session_sticky":{"ttl":"999999999999999999h"}}`, `{"session_sticky":{"gc_interval":"1h"}}`,
		`{"desktop":null}`, `{"desktop":{"proxy_url":false}}`, `{"desktop":{"unknown":"x"}}`,
	}
	for _, proxy := range []string{"https://localhost:8000", "socks5://localhost:8000", "http://u:p@localhost:8000", "http://localhost", "http://localhost:0", "http://localhost:65536", "http://localhost:8000/", "http://localhost:8000/x", "http://localhost:8000?", "http://localhost:8000?x=1", "http://localhost:8000#", "http://localhost:8000#x", "http://:8000", "http://bad_host:8000", "http://-host:8000", " http://localhost:8000", "http://localhost:+80", "http://localhost:080", "http://[not-ip]:8000"} {
		raw, _ := json.Marshal(proxy)
		patches = append(patches, `{"desktop":{"proxy_url":`+string(raw)+`}}`)
	}
	for i, patch := range patches {
		if _, err := s.Prepare(context.Background(), revision, []byte(patch)); !errors.Is(err, ErrDesktopConfigInvalid) {
			t.Errorf("case %d: accepted invalid patch or wrong error: %v", i, err)
		}
	}
	if got, _ := os.ReadFile(s.path); string(got) != localDesktopConfig(s) {
		t.Fatal("invalid patch wrote config")
	}
	if _, err := os.Stat(s.backupDir); !os.IsNotExist(err) {
		t.Fatalf("invalid patch created a backup directory: %v", err)
	}
}

func TestDesktopConfigDefaultsAndValidBoundaries(t *testing.T) {
	s := newDesktopConfigFixture(t, strings.Replace(desktopConfigFixture, `"device_token":`, `"timeout_seconds":45,"header_timeout_seconds":0,"idle_timeout_seconds":-1,"device_token":`, 1))
	view := readDesktopFixture(t, s)
	if view.Editable.Upstream.TimeoutSeconds != 45 || view.Editable.Upstream.HeaderTimeoutSeconds != 45 || view.Editable.Upstream.IdleTimeoutSeconds != 300 {
		t.Fatal("effective timeout fallback differs from gateway")
	}
	for _, patch := range []string{
		`{"port":1024,"pool":{"max_in_flight":0},"upstream":{"timeout_seconds":1},"session_sticky":{"ttl":"1s"}}`,
		`{"port":65535,"upstream":{"idle_timeout_seconds":86400},"session_sticky":{"ttl":"720h"}}`,
		`{"desktop":{"proxy_url":""}}`, `{"desktop":{"proxy_url":"http://localhost:1"}}`, `{"desktop":{"proxy_url":"http://[::1]:65535"}}`,
	} {
		tx := mustPrepareDesktop(t, s, patch)
		if _, err := s.Commit(context.Background(), tx.BackupID); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(tx.BackupID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDesktopConfigConcurrentRevisionOneWinner(t *testing.T) {
	s := newDesktopConfigFixture(t, desktopConfigFixture)
	revision := readDesktopFixture(t, s).Revision
	start := make(chan struct{})
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			<-start
			tx, err := s.Prepare(context.Background(), revision, []byte(fmt.Sprintf(`{"port":%d}`, port)))
			if errors.Is(err, ErrDesktopConfigConflict) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := s.Commit(context.Background(), tx.BackupID); err != nil {
				t.Error(err)
				return
			}
			wins.Add(1)
			if err := s.Finish(tx.BackupID); err != nil {
				t.Error(err)
			}
		}(9000 + i)
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("same revision committed %d times", wins.Load())
	}
}

func TestDesktopConfigExternalEditConflictsAndKeepsRecovery(t *testing.T) {
	for _, stage := range []string{"prepare", "commit", "restore"} {
		t.Run(stage, func(t *testing.T) {
			s := newDesktopConfigFixture(t, desktopConfigFixture)
			before := readDesktopFixture(t, s)
			var tx PreparedDesktopConfig
			if stage != "prepare" {
				tx = mustPrepareDesktop(t, s, `{"port":9001}`)
			}
			if stage == "restore" {
				if _, err := s.Commit(context.Background(), tx.BackupID); err != nil {
					t.Fatal(err)
				}
			}
			external := strings.Replace(localDesktopConfig(s), "7863", "9010", 1)
			if err := os.WriteFile(s.path, []byte(external), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch stage {
			case "prepare":
				_, err = s.Prepare(context.Background(), before.Revision, []byte(`{"port":9002}`))
			case "commit":
				_, err = s.Commit(context.Background(), tx.BackupID)
			case "restore":
				_, err = s.Restore(context.Background(), tx.BackupID, tx.CandidateRevision)
			}
			if !errors.Is(err, ErrDesktopConfigConflict) {
				t.Fatalf("external edit did not conflict: %v", err)
			}
			if got, _ := os.ReadFile(s.path); string(got) != external {
				t.Fatal("overwrote external edit")
			}
			if stage == "restore" {
				if s.active == nil {
					t.Fatal("failed restore lost protected transaction")
				}
				if raw, err := os.ReadFile(s.backupPath(tx.BackupID)); err != nil || string(raw) != localDesktopConfig(s) {
					t.Fatal("failed restore lost original backup")
				}
				if err := s.Finish(tx.BackupID); err != nil {
					t.Fatal(err)
				}
			} else if s.active != nil {
				t.Fatal("failed pre-commit transaction remained active")
			}
		})
	}
}

// Strict writer checks ctx immediately before both beforeCommit calls. Inject
// only at the second check, after temporary-file IO, without a production hook.
type desktopConfigCheckContext struct {
	context.Context
	checks int
	second func()
	err    error
}

func (c *desktopConfigCheckContext) Err() error {
	c.checks++
	if c.checks == 2 {
		c.second()
	}
	return c.err
}

func TestDesktopConfigStrictWriteRechecksRevisionAndPreservesOldOnFailure(t *testing.T) {
	for _, cause := range []string{"external", "cancel"} {
		t.Run(cause, func(t *testing.T) {
			s := newDesktopConfigFixture(t, desktopConfigFixture)
			tx := mustPrepareDesktop(t, s, `{"port":9001}`)
			ctx := &desktopConfigCheckContext{Context: context.Background()}
			expected := localDesktopConfig(s)
			ctx.second = func() {
				if cause == "cancel" {
					ctx.err = context.Canceled
					return
				}
				expected = strings.Replace(localDesktopConfig(s), "7863", "9010", 1)
				if err := os.WriteFile(s.path, []byte(expected), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.Commit(ctx, tx.BackupID)
			if cause == "cancel" && !errors.Is(err, context.Canceled) || cause == "external" && !errors.Is(err, ErrDesktopConfigConflict) {
				t.Fatalf("wrong strict write error: %v", err)
			}
			if ctx.checks != 2 {
				t.Fatalf("did not inject after temporary IO: %d", ctx.checks)
			}
			if raw, _ := os.ReadFile(s.path); string(raw) != expected {
				t.Fatal("failed write damaged old file")
			}
			entries, err := os.ReadDir(filepath.Dir(s.path))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".workbuddy-") {
					t.Fatal("temporary file remained")
				}
			}
		})
	}
}

func TestDesktopConfigKeyRotationBoundToTransaction(t *testing.T) {
	s := newDesktopConfigFixture(t, desktopConfigFixture)
	revision := readDesktopFixture(t, s).Revision
	if _, err := s.PrepareKeyRotation(context.Background(), revision, bytes.Repeat([]byte{1}, 31)); !errors.Is(err, ErrDesktopConfigInvalid) {
		t.Fatal("short key accepted")
	}
	key := bytes.Repeat([]byte{0x71}, 32) // Synthetic entropy supplied only by this test.
	tx, err := s.PrepareKeyRotation(context.Background(), revision, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(context.Background(), "../../other-config"); !errors.Is(err, ErrDesktopConfigTransaction) {
		t.Fatal("unbound backup ID accepted")
	}
	view, err := s.Commit(context.Background(), tx.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.path)
	if !bytes.Contains(raw, []byte(base64.RawURLEncoding.EncodeToString(key))) || bytes.Contains(raw, []byte("FAKE_CONFIG_KEY_DO_NOT_EXPORT")) {
		t.Fatal("internal key rotation did not replace key")
	}
	encoded, _ := json.Marshal(view)
	if bytes.Contains(encoded, []byte(base64.RawURLEncoding.EncodeToString(key))) {
		t.Fatal("rotated key escaped into projection")
	}
	if _, err := s.Restore(context.Background(), tx.BackupID, revision); !errors.Is(err, ErrDesktopConfigConflict) {
		t.Fatal("wrong current revision accepted")
	}
	if _, err := s.Restore(context.Background(), tx.BackupID, view.Revision); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(s.path); string(got) != localDesktopConfig(s) {
		t.Fatal("key rollback not byte exact")
	}
}

func TestDesktopConfigRejectsUnsafeExistingFiles(t *testing.T) {
	for _, kind := range []string{"malformed", "null", "nonloopback", "missing-schedules", "enabled-schedule", "admin", "case-alias", "duplicate", "wrong-type", "symlink", "directory-symlink", "file-mode", "directory-mode"} {
		t.Run(kind, func(t *testing.T) {
			s := newDesktopConfigFixture(t, desktopConfigFixture)
			original := localDesktopConfig(s)
			raw := original
			switch kind {
			case "malformed":
				raw = `{"listen":`
			case "null":
				raw = `null`
			case "nonloopback":
				raw = strings.Replace(raw, "127.0.0.1", "0.0.0.0", 1)
			case "missing-schedules":
				raw = strings.Replace(raw, `"checkin_enabled":false,`, "", 1)
			case "enabled-schedule":
				raw = strings.Replace(raw, `"travel_enabled":false`, `"travel_enabled":true`, 1)
			case "admin":
				raw = strings.Replace(raw, `"admin": {"enabled":false}`, `"admin": {"enabled":true}`, 1)
			case "case-alias":
				raw = strings.Replace(raw, `"listen":`, `"LISTEN":"0.0.0.0:7863","listen":`, 1)
			case "duplicate":
				raw = strings.Replace(raw, `"listen":`, `"listen":"0.0.0.0:7863","listen":`, 1)
			case "wrong-type":
				raw = strings.Replace(raw, `"device_token":`, `"timeout_seconds":null,"device_token":`, 1)
			case "symlink":
				target := filepath.Join(filepath.Dir(s.path), "untouched.json")
				if err := os.Rename(s.path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, s.path); err != nil {
					t.Fatal(err)
				}
			case "directory-symlink":
				alias := filepath.Join(t.TempDir(), "private-link")
				if err := os.Symlink(filepath.Dir(s.path), alias); err != nil {
					t.Fatal(err)
				}
				s.path = filepath.Join(alias, "config.json")
			case "file-mode":
				if err := os.Chmod(s.path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "directory-mode":
				if err := os.Chmod(filepath.Dir(s.path), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if raw != original {
				if err := os.WriteFile(s.path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Read(context.Background()); err == nil {
				t.Fatal("unsafe old config accepted")
			} else if strings.Contains(err.Error(), filepath.Dir(s.path)) || strings.Contains(err.Error(), "FAKE_") {
				t.Fatal("error exposed path or credential")
			}
			if _, err := s.Prepare(context.Background(), configRevision([]byte(raw)), []byte(`{"port":9000}`)); err == nil {
				t.Fatal("unsafe old config prepared")
			}
		})
	}
}

func TestDesktopConfigBackupRetentionAndBudget(t *testing.T) {
	t.Run("prune-oldest-and-preserve-other-files", func(t *testing.T) {
		s := newDesktopConfigFixture(t, desktopConfigFixture)
		if err := os.Mkdir(s.backupDir, 0o700); err != nil {
			t.Fatal(err)
		}
		foreign := filepath.Join(s.backupDir, "account.json")
		if err := os.WriteFile(foreign, []byte("synthetic-account-not-a-config"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			id := fmt.Sprintf("%032x", i+1)
			path := s.backupPath(id)
			if err := os.WriteFile(path, []byte(localDesktopConfig(s)), 0o600); err != nil {
				t.Fatal(err)
			}
			when := time.Unix(int64(i+1), 0)
			if err := os.Chtimes(path, when, when); err != nil {
				t.Fatal(err)
			}
		}
		tx := mustPrepareDesktop(t, s, `{"port":9000}`)
		if _, err := os.Stat(s.backupPath(fmt.Sprintf("%032x", 1))); !os.IsNotExist(err) {
			t.Fatal("oldest backup not pruned")
		}
		if _, err := os.Stat(s.backupPath(fmt.Sprintf("%032x", 10))); err != nil {
			t.Fatal("latest valid backup pruned")
		}
		if _, err := os.Stat(s.backupPath(tx.BackupID)); err != nil {
			t.Fatal("active backup missing")
		}
		if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "synthetic-account-not-a-config" {
			t.Fatal("non-config backup modified")
		}
		entries, _ := os.ReadDir(s.backupDir)
		if len(entries) != 11 {
			t.Fatalf("backup count escaped cap: %d", len(entries))
		}
		if err := s.Finish(tx.BackupID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("protect-valid-version-before-budget-rejection", func(t *testing.T) {
		large := strings.TrimSpace(desktopConfigFixture)
		large = large[:len(large)-1] + `,"padding":"` + strings.Repeat("x", 6*1024*1024) + `"}`
		s := newDesktopConfigFixture(t, large)
		large = strings.ReplaceAll(large, "/Users/fake-private-user", filepath.Dir(s.path))
		if err := os.Mkdir(s.backupDir, 0o700); err != nil {
			t.Fatal(err)
		}
		id := strings.Repeat("a", 32)
		if err := os.WriteFile(s.backupPath(id), []byte(large), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Prepare(context.Background(), readDesktopFixture(t, s).Revision, []byte(`{"port":9000}`)); !errors.Is(err, ErrDesktopConfigBudget) {
			t.Fatalf("impossible protected budget accepted: %v", err)
		}
		if raw, err := os.ReadFile(s.backupPath(id)); err != nil || string(raw) != large {
			t.Fatal("budget failure deleted latest valid backup")
		}
		if got, _ := os.ReadFile(s.path); string(got) != large {
			t.Fatal("budget failure damaged current config")
		}
		entries, _ := os.ReadDir(s.backupDir)
		if len(entries) != 1 || s.active != nil {
			t.Fatal("budget rejection left candidate backup or active transaction")
		}
	})
}

func TestDesktopConfigTamperedBackupPreventsCommitOrRestore(t *testing.T) {
	for _, stage := range []string{"commit", "restore"} {
		t.Run(stage, func(t *testing.T) {
			s := newDesktopConfigFixture(t, desktopConfigFixture)
			tx := mustPrepareDesktop(t, s, `{"port":9000}`)
			if stage == "restore" {
				if _, err := s.Commit(context.Background(), tx.BackupID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(s.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.backupPath(tx.BackupID), []byte(strings.Replace(localDesktopConfig(s), "7863", "9100", 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			if stage == "commit" {
				_, err = s.Commit(context.Background(), tx.BackupID)
			} else {
				_, err = s.Restore(context.Background(), tx.BackupID, tx.CandidateRevision)
			}
			if !errors.Is(err, ErrDesktopConfigConflict) {
				t.Fatalf("changed backup did not conflict: %v", err)
			}
			if after, err := os.ReadFile(s.path); err != nil || !bytes.Equal(before, after) {
				t.Fatal("tampered backup changed current config")
			}
			if stage == "restore" && s.active == nil {
				t.Fatal("failed restore released backup protection")
			}
		})
	}
}

func TestDesktopConfigBackupFilesystemFailsClosed(t *testing.T) {
	for _, kind := range []string{"directory-permissions", "directory-symlink", "backup-symlink", "backup-permissions"} {
		t.Run(kind, func(t *testing.T) {
			s := newDesktopConfigFixture(t, desktopConfigFixture)
			if kind == "directory-symlink" {
				if err := os.Symlink(t.TempDir(), s.backupDir); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(s.backupDir, 0o700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "directory-permissions":
					if err := os.Chmod(s.backupDir, 0o755); err != nil {
						t.Fatal(err)
					}
				case "backup-symlink":
					if err := os.Symlink(s.path, s.backupPath(strings.Repeat("b", 32))); err != nil {
						t.Fatal(err)
					}
				case "backup-permissions":
					if err := os.WriteFile(s.backupPath(strings.Repeat("b", 32)), []byte(localDesktopConfig(s)), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.Prepare(context.Background(), readDesktopFixture(t, s).Revision, []byte(`{"port":9000}`)); !errors.Is(err, ErrDesktopConfigStorage) {
				t.Fatalf("unsafe backup directory accepted: %v", err)
			}
			if raw, err := os.ReadFile(s.path); err != nil || string(raw) != localDesktopConfig(s) {
				t.Fatal("backup failure damaged old config")
			}
		})
	}
}

func TestDesktopConfigProtectedPathsRejectBeforeBackup(t *testing.T) {
	for _, field := range []string{"auth_dir", "state_file"} {
		for _, kind := range []string{"missing", "empty", "relative", "outside", "traversal", "symlink"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				s := newDesktopConfigFixture(t, desktopConfigFixture)
				doc, err := configObject([]byte(localDesktopConfig(s)))
				if err != nil {
					t.Fatal(err)
				}
				var expected string
				if err := json.Unmarshal(doc[field], &expected); err != nil {
					t.Fatal(err)
				}
				value := ""
				switch kind {
				case "relative":
					value = filepath.Base(expected)
				case "outside":
					value = filepath.Join(t.TempDir(), filepath.Base(expected))
				case "traversal":
					value = filepath.Dir(s.path) + "/../" + filepath.Base(expected)
				case "symlink":
					value = expected
					if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), expected); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "missing" {
					delete(doc, field)
				} else {
					doc[field], _ = json.Marshal(value)
				}
				original, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(s.path, original, 0o600); err != nil {
					t.Fatal(err)
				}
				view := readDesktopFixture(t, s) // Diagnostics remain available.
				if _, err := s.Prepare(context.Background(), view.Revision, []byte(`{"port":9000}`)); !errors.Is(err, ErrDesktopConfigInvalid) {
					t.Fatalf("ordinary prepare accepted unsafe path: %v", err)
				}
				if _, err := s.PrepareKeyRotation(context.Background(), view.Revision, bytes.Repeat([]byte{1}, 32)); !errors.Is(err, ErrDesktopConfigInvalid) {
					t.Fatalf("rotation accepted unsafe path: %v", err)
				}
				if raw, err := os.ReadFile(s.path); err != nil || !bytes.Equal(raw, original) {
					t.Fatal("rejected path changed original bytes")
				}
				if _, err := os.Stat(s.backupDir); !os.IsNotExist(err) || s.active != nil {
					t.Fatal("rejected path created backup or active transaction")
				}
			})
		}
	}
}

func TestDesktopConfigEmptyKeyRequiresInternalRotation(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "blank"} {
		t.Run(kind, func(t *testing.T) {
			s := newDesktopConfigFixture(t, desktopConfigFixture)
			doc, err := configObject([]byte(localDesktopConfig(s)))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing" {
				delete(doc, "api_key")
			} else if kind == "empty" {
				doc["api_key"] = json.RawMessage(`""`)
			} else {
				doc["api_key"] = json.RawMessage(`" \t\n"`)
			}
			original, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			view := readDesktopFixture(t, s)
			if view.KeyConfigured {
				t.Fatal("diagnostic projection hides unconfigured key")
			}
			if _, err := s.Prepare(context.Background(), view.Revision, []byte(`{"port":9000}`)); !errors.Is(err, ErrDesktopConfigInvalid) {
				t.Fatalf("ordinary prepare accepted empty key: %v", err)
			}
			if raw, err := os.ReadFile(s.path); err != nil || !bytes.Equal(raw, original) {
				t.Fatal("rejected empty key changed original bytes")
			}
			if _, err := os.Stat(s.backupDir); !os.IsNotExist(err) || s.active != nil {
				t.Fatal("rejected key created backup or active transaction")
			}
			// State is not created by the store and need not exist on first use.
			if _, err := os.Stat(filepath.Join(filepath.Dir(s.path), "state.json")); !os.IsNotExist(err) {
				t.Fatal("test must start without state file")
			}
			tx, err := s.PrepareKeyRotation(context.Background(), view.Revision, bytes.Repeat([]byte{1}, 32))
			if err != nil {
				t.Fatal(err)
			}
			repaired, err := s.Commit(context.Background(), tx.BackupID)
			if err != nil || !repaired.KeyConfigured {
				t.Fatalf("internal rotation did not repair key: %v", err)
			}
			if err := s.Finish(tx.BackupID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDesktopConfigCommitRechecksProtectedPaths(t *testing.T) {
	for _, name := range []string{"auths", "state.json"} {
		for _, stage := range []string{"before-commit", "before-rename"} {
			t.Run(name+"/"+stage, func(t *testing.T) {
				s := newDesktopConfigFixture(t, desktopConfigFixture)
				path := filepath.Join(filepath.Dir(s.path), name)
				if name == "auths" {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
				tx := mustPrepareDesktop(t, s, `{"port":9000}`)
				swap := func() {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
						t.Fatal(err)
					}
				}
				var ctx context.Context = context.Background()
				if stage == "before-commit" {
					swap()
				} else {
					ctx = &desktopConfigCheckContext{Context: context.Background(), second: swap}
				}
				if _, err := s.Commit(ctx, tx.BackupID); err == nil {
					t.Fatal("commit accepted a replaced protected path")
				}
				if raw, err := os.ReadFile(s.path); err != nil || string(raw) != localDesktopConfig(s) {
					t.Fatal("rejected commit changed original config bytes")
				}
				if s.active != nil {
					t.Fatal("failed commit retained prepared transaction")
				}
				if _, err := s.Commit(context.Background(), tx.BackupID); !errors.Is(err, ErrDesktopConfigTransaction) {
					t.Fatalf("failed transaction can still commit: %v", err)
				}
			})
		}
	}
}
