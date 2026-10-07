package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"workbuddy2api-gui/internal/fsutil"
)

var (
	ErrDesktopConfigConflict    = errors.New("desktop config revision conflict") // HTTP 409 at the API boundary.
	ErrDesktopConfigInvalid     = errors.New("invalid desktop config")
	ErrDesktopConfigStorage     = errors.New("desktop config storage unavailable")
	ErrDesktopConfigBudget      = errors.New("desktop config backup budget exceeded")
	ErrDesktopConfigTransaction = errors.New("invalid desktop config transaction")
)

const (
	desktopConfigBackupLimit       = 10
	desktopConfigByteLimit   int64 = 10 * 1024 * 1024
	desktopBackupPrefix            = "desktop-config-"
)

type DesktopEditableConfig struct {
	Port     int  `json:"port"`
	AllowLAN bool `json:"allow_lan"`
	Upstream struct {
		TimeoutSeconds       int `json:"timeout_seconds"`
		HeaderTimeoutSeconds int `json:"header_timeout_seconds"`
		IdleTimeoutSeconds   int `json:"idle_timeout_seconds"`
	} `json:"upstream"`
	Pool struct {
		MaxInFlight int `json:"max_in_flight"`
	} `json:"pool"`
	SessionSticky struct {
		Enabled bool   `json:"enabled"`
		TTL     string `json:"ttl"`
	} `json:"session_sticky"`
	Schedule struct {
		CheckinEnabled bool  `json:"checkin_enabled"`
		CheckinHours   []int `json:"checkin_hours"`
	} `json:"schedule"`
	Desktop struct {
		ProxyURL string `json:"proxy_url"`
	} `json:"desktop"`
}

// DesktopConfigView is the only configuration payload intended for a web page.
// Never add raw configuration, credentials, file paths or parser errors here.
type DesktopConfigView struct {
	Revision    string                `json:"revision"`
	Editable    DesktopEditableConfig `json:"editable"`
	SafeSummary struct {
		ListenHost        string `json:"listen_host"`
		SchedulesDisabled bool   `json:"schedules_disabled"`
	} `json:"safe_summary"`
	KeyConfigured bool `json:"key_configured"`
}

type PreparedDesktopConfig struct {
	BackupID          string `json:"backup_id"`
	Revision          string `json:"revision"`
	CandidateRevision string `json:"candidate_revision"`
}

type desktopConfigTransaction struct {
	PreparedDesktopConfig
	candidate []byte
	committed bool
}

// DesktopConfigStore is separate from the legacy Web/Docker configuration path.
// Keep one store per config file. Its single active transaction spans gateway
// shutdown, commit, application verification and (when needed) rollback.
type DesktopConfigStore struct {
	mu        sync.Mutex
	path      string
	backupDir string
	active    *desktopConfigTransaction
}

func NewDesktopConfigStore(path, backupDir string) (*DesktopConfigStore, error) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(backupDir) || filepath.Clean(path) == filepath.Clean(backupDir) {
		return nil, ErrDesktopConfigStorage
	}
	s := &DesktopConfigStore{path: filepath.Clean(path), backupDir: filepath.Clean(backupDir)}
	if _, _, err := s.readCurrent(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *DesktopConfigStore) Read(ctx context.Context) (DesktopConfigView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return DesktopConfigView{}, err
	}
	_, view, err := s.readCurrent()
	return view, err
}

// Prepare accepts only editable fields, never a complete config or API key.
func (s *DesktopConfigStore) Prepare(ctx context.Context, revision string, patch []byte) (PreparedDesktopConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepare(ctx, revision, patch, nil)
}

// PrepareKeyRotation is for the authenticated internal control path only. The
// caller must supply bytes from a checked cryptographically secure random source;
// the web patch cannot call or supply a key to this operation.
func (s *DesktopConfigStore) PrepareKeyRotation(ctx context.Context, revision string, randomKey []byte) (PreparedDesktopConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(randomKey) < 32 || len(randomKey) > 1024 {
		return PreparedDesktopConfig{}, invalidConfig("api_key")
	}
	return s.prepare(ctx, revision, nil, randomKey)
}

func (s *DesktopConfigStore) prepare(ctx context.Context, revision string, patch, randomKey []byte) (PreparedDesktopConfig, error) {
	if err := ctx.Err(); err != nil {
		return PreparedDesktopConfig{}, err
	}
	if s.active != nil {
		return PreparedDesktopConfig{}, ErrDesktopConfigConflict
	}
	raw, view, err := s.readCurrent()
	if err != nil {
		return PreparedDesktopConfig{}, err
	}
	if view.Revision != revision {
		return PreparedDesktopConfig{}, ErrDesktopConfigConflict
	}
	doc, err := configObject(raw)
	if err != nil {
		return PreparedDesktopConfig{}, err
	}
	if randomKey != nil {
		doc["api_key"], _ = json.Marshal(base64.RawURLEncoding.EncodeToString(randomKey))
	} else if err := mergeDesktopPatch(doc, patch); err != nil {
		return PreparedDesktopConfig{}, err
	}
	if err := s.validateProtectedConfig(doc); err != nil {
		return PreparedDesktopConfig{}, err
	}
	candidate, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return PreparedDesktopConfig{}, ErrDesktopConfigInvalid
	}
	candidate = append(candidate, '\n')
	if int64(len(candidate)) > desktopConfigByteLimit {
		return PreparedDesktopConfig{}, ErrDesktopConfigBudget
	}
	if _, err := desktopConfigProjection(candidate); err != nil {
		return PreparedDesktopConfig{}, err
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return PreparedDesktopConfig{}, ErrDesktopConfigStorage
	}
	id := hex.EncodeToString(idBytes[:])
	if err := s.reserveBackup(ctx, id, raw, revision); err != nil {
		return PreparedDesktopConfig{}, err
	}
	prepared := PreparedDesktopConfig{BackupID: id, Revision: revision, CandidateRevision: configRevision(candidate)}
	s.active = &desktopConfigTransaction{PreparedDesktopConfig: prepared, candidate: candidate}
	return prepared, nil
}

// AppPaths and GatewayConfigFile put these targets directly under the trusted
// config directory. A read can still diagnose a broken config, but an apply must
// not inherit missing credentials, another installation's paths or symlinks.
func (s *DesktopConfigStore) validateProtectedConfig(doc map[string]json.RawMessage) error {
	var key string
	if err := configValue(doc, "api_key", &key); err != nil || strings.TrimSpace(key) == "" {
		return invalidConfig("api_key")
	}
	for field, base := range map[string]string{"auth_dir": "auths", "state_file": "state.json"} {
		var path string
		if err := configValue(doc, field, &path); err != nil || path != filepath.Join(filepath.Dir(s.path), base) {
			return invalidConfig(field)
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue // The first run may not have created auths or state.json yet.
		}
		if err != nil || field == "auth_dir" && !info.IsDir() || field == "state_file" && !info.Mode().IsRegular() {
			return invalidConfig(field)
		}
	}
	return nil
}

func (s *DesktopConfigStore) Commit(ctx context.Context, backupID string) (DesktopConfigView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := s.active
	if tx == nil || tx.BackupID != backupID || tx.committed {
		return DesktopConfigView{}, ErrDesktopConfigTransaction
	}
	backup, err := readPrivateConfig(s.backupPath(backupID))
	if err != nil || configRevision(backup) != tx.Revision {
		s.active = nil
		if err != nil {
			return DesktopConfigView{}, err
		}
		return DesktopConfigView{}, ErrDesktopConfigConflict
	}
	// A failed strict write leaves the previous file intact and ends preparation.
	candidate, err := configObject(tx.candidate)
	if err == nil {
		err = fsutil.WriteFileAtomicStrict(ctx, s.path, tx.candidate, 0o600, func() error {
			if err := s.checkRevision(tx.Revision); err != nil {
				return err
			}
			return s.validateProtectedConfig(candidate)
		})
	}
	if err != nil {
		s.active = nil
		return DesktopConfigView{}, desktopStorageError(err)
	}
	tx.committed = true
	return desktopConfigProjection(tx.candidate)
}

// Restore requires the active backup and the exact revision returned by Commit.
// On failure the transaction and its protected backup remain available. The
// caller must explicitly Finish after deciding how to surface/recover the failure.
func (s *DesktopConfigStore) Restore(ctx context.Context, backupID, currentRevision string) (DesktopConfigView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := s.active
	if tx == nil || tx.BackupID != backupID || !tx.committed {
		return DesktopConfigView{}, ErrDesktopConfigTransaction
	}
	if currentRevision != tx.CandidateRevision {
		return DesktopConfigView{}, ErrDesktopConfigConflict
	}
	raw, err := readPrivateConfig(s.backupPath(backupID))
	if err != nil {
		return DesktopConfigView{}, err
	}
	if configRevision(raw) != tx.Revision {
		return DesktopConfigView{}, ErrDesktopConfigConflict
	}
	view, err := desktopConfigProjection(raw)
	if err != nil {
		return DesktopConfigView{}, err
	}
	if err := s.replace(ctx, currentRevision, raw); err != nil {
		return DesktopConfigView{}, err
	}
	s.active = nil
	return view, nil
}

// Finish ends an aborted prepare or an application attempt whose final outcome
// is known. A successful Commit alone MUST NOT call Finish before gateway
// verification; otherwise the caller loses this transaction's rollback handle.
func (s *DesktopConfigStore) Finish(backupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.BackupID != backupID {
		return ErrDesktopConfigTransaction
	}
	s.active = nil
	return nil
}

func (s *DesktopConfigStore) readCurrent() ([]byte, DesktopConfigView, error) {
	raw, err := readPrivateConfig(s.path)
	if err != nil {
		return nil, DesktopConfigView{}, err
	}
	view, err := desktopConfigProjection(raw)
	return raw, view, err
}

func (s *DesktopConfigStore) checkRevision(revision string) error {
	raw, err := readPrivateConfig(s.path)
	if err != nil {
		return err
	}
	if configRevision(raw) != revision {
		return ErrDesktopConfigConflict
	}
	return nil
}

func (s *DesktopConfigStore) replace(ctx context.Context, revision string, raw []byte) error {
	if err := s.checkRevision(revision); err != nil {
		return err
	}
	err := fsutil.WriteFileAtomicStrict(ctx, s.path, raw, 0o600, func() error { return s.checkRevision(revision) })
	return desktopStorageError(err)
}

func configRevision(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func invalidConfig(field string) error { return fmt.Errorf("%w: %s", ErrDesktopConfigInvalid, field) }

// Do not expose os.PathError or JSON parser details to the eventual HTTP caller.
func desktopStorageError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrDesktopConfigConflict) || errors.Is(err, ErrDesktopConfigBudget) {
		return err
	}
	return ErrDesktopConfigStorage
}

func privateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || fsutil.PermBitsEnforced() && info.Mode().Perm() != 0o700 ||
		fsutil.CheckPrivateACL(path) != nil {
		return ErrDesktopConfigStorage
	}
	return nil
}

func readPrivateConfig(path string) ([]byte, error) {
	if err := privateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || fsutil.PermBitsEnforced() && before.Mode().Perm() != 0o600 {
		return nil, ErrDesktopConfigStorage
	}
	raw, err := fsutil.ReadFileStrict(path, desktopConfigByteLimit)
	if err != nil {
		return nil, desktopStorageError(err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || fsutil.PermBitsEnforced() && after.Mode().Perm() != 0o600 {
		return nil, ErrDesktopConfigStorage
	}
	return raw, nil
}

// Parsing into RawMessage preserves unedited integers, including values larger
// than float64 or int64. Reject duplicate object keys instead of silently picking
// a different meaning from another reader. Unknown legacy values stay opaque.
func configObject(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, ErrDesktopConfigInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, ErrDesktopConfigInvalid
	}
	result := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, ErrDesktopConfigInvalid
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrDesktopConfigInvalid
		}
		if _, exists := result[key]; exists {
			return nil, ErrDesktopConfigInvalid
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, ErrDesktopConfigInvalid
		}
		result[key] = value
	}
	return result, nil
}

func configSection(doc map[string]json.RawMessage, name string) (map[string]json.RawMessage, error) {
	raw, ok := doc[name]
	if !ok {
		return map[string]json.RawMessage{}, nil
	}
	section, err := configObject(raw)
	if err != nil {
		return nil, invalidConfig(name)
	}
	return section, nil
}

func configValue(doc map[string]json.RawMessage, key string, target any) error {
	raw, ok := doc[key]
	if !ok {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
		return invalidConfig(key)
	}
	return nil
}

// Go's gateway decoder matches struct fields case-insensitively. An alternate
// spelling must not bypass a safety check or shadow a canonical edited field.
func canonicalConfigKeys(doc map[string]json.RawMessage, keys ...string) error {
	for key := range doc {
		for _, known := range keys {
			if key != known && strings.EqualFold(key, known) {
				return ErrDesktopConfigInvalid
			}
		}
	}
	return nil
}

func desktopConfigProjection(raw []byte) (DesktopConfigView, error) {
	var view DesktopConfigView
	doc, err := configObject(raw)
	if err != nil {
		return view, err
	}
	if err := canonicalConfigKeys(doc, "listen", "api_key", "auth_dir", "state_file", "admin", "schedule", "upstream", "pool", "session_sticky", "desktop"); err != nil {
		return view, err
	}
	var listen, key string
	if err := configValue(doc, "listen", &listen); err != nil {
		return view, err
	}
	host, port, err := parseDesktopListen(listen)
	if err != nil {
		return view, err
	}
	view.Editable.Port, view.Editable.AllowLAN = port, host == "0.0.0.0"
	if err := configValue(doc, "api_key", &key); err != nil {
		return view, err
	}
	view.KeyConfigured = strings.TrimSpace(key) != ""
	if view.Editable.AllowLAN && !view.KeyConfigured {
		return view, invalidConfig("api_key")
	}
	for _, name := range []string{"auth_dir", "state_file"} {
		var path string
		if err := configValue(doc, name, &path); err != nil {
			return view, err
		}
	}
	admin, err := configSection(doc, "admin")
	if err != nil {
		return view, err
	}
	if err := canonicalConfigKeys(admin, "enabled"); err != nil {
		return view, err
	}
	var adminEnabled bool
	if err := configValue(admin, "enabled", &adminEnabled); err != nil || adminEnabled {
		return view, invalidConfig("admin.enabled")
	}
	schedule, err := configSection(doc, "schedule")
	if err != nil {
		return view, err
	}
	scheduleKeys := []string{"checkin_enabled", "checkin_hours", "travel_enabled", "activity_enabled", "keepalive_enabled", "school_enabled", "cat_enabled"}
	if err := canonicalConfigKeys(schedule, scheduleKeys...); err != nil {
		return view, err
	}
	// Gateway defaults a missing switch to true; the desktop requires an explicit
	// check-in choice and keeps the other five schedules disabled.
	if _, ok := schedule["checkin_enabled"]; !ok {
		return view, invalidConfig("schedule.checkin_enabled")
	}
	if err := configValue(schedule, "checkin_enabled", &view.Editable.Schedule.CheckinEnabled); err != nil {
		return view, err
	}
	for _, name := range scheduleKeys[2:] {
		enabled := true
		if err := configValue(schedule, name, &enabled); err != nil || enabled {
			return view, invalidConfig("schedule")
		}
	}
	view.Editable.Schedule.CheckinHours = []int{9, 21}
	if rawHours, ok := schedule["checkin_hours"]; ok && !bytes.Equal(bytes.TrimSpace(rawHours), []byte("null")) {
		var hours []int
		if err := configValue(schedule, "checkin_hours", &hours); err != nil {
			return view, err
		}
		if len(hours) != 0 {
			if !validCheckinHours(hours) {
				return view, invalidConfig("schedule.checkin_hours")
			}
			view.Editable.Schedule.CheckinHours = hours
		}
	}
	upstream, err := configSection(doc, "upstream")
	if err != nil {
		return view, err
	}
	if err := canonicalConfigKeys(upstream, "timeout_seconds", "header_timeout_seconds", "idle_timeout_seconds"); err != nil {
		return view, err
	}
	u := &view.Editable.Upstream
	u.TimeoutSeconds = 120
	for name, dst := range map[string]*int{"timeout_seconds": &u.TimeoutSeconds, "header_timeout_seconds": &u.HeaderTimeoutSeconds, "idle_timeout_seconds": &u.IdleTimeoutSeconds} {
		if err := configValue(upstream, name, dst); err != nil {
			return view, err
		}
	}
	// Defaults and <=0 fallbacks match gateway/cmd/server/config.go normalize.
	if u.TimeoutSeconds <= 0 {
		u.TimeoutSeconds = 120
	}
	if u.HeaderTimeoutSeconds <= 0 {
		u.HeaderTimeoutSeconds = u.TimeoutSeconds
	}
	if u.IdleTimeoutSeconds <= 0 {
		u.IdleTimeoutSeconds = 300
	}
	pool, err := configSection(doc, "pool")
	if err != nil {
		return view, err
	}
	if err := canonicalConfigKeys(pool, "max_in_flight"); err != nil {
		return view, err
	}
	view.Editable.Pool.MaxInFlight = 3
	if err := configValue(pool, "max_in_flight", &view.Editable.Pool.MaxInFlight); err != nil {
		return view, err
	}
	if view.Editable.Pool.MaxInFlight < 0 {
		return view, invalidConfig("pool.max_in_flight")
	}
	sticky, err := configSection(doc, "session_sticky")
	if err != nil {
		return view, err
	}
	if err := canonicalConfigKeys(sticky, "enabled", "ttl"); err != nil {
		return view, err
	}
	view.Editable.SessionSticky.Enabled, view.Editable.SessionSticky.TTL = true, "30m"
	if err := configValue(sticky, "enabled", &view.Editable.SessionSticky.Enabled); err != nil {
		return view, err
	}
	if err := configValue(sticky, "ttl", &view.Editable.SessionSticky.TTL); err != nil {
		return view, err
	}
	if _, err := time.ParseDuration(view.Editable.SessionSticky.TTL); err != nil {
		return view, invalidConfig("session_sticky.ttl")
	}
	desktop, err := configSection(doc, "desktop")
	if err != nil {
		return view, err
	}
	if err := canonicalConfigKeys(desktop, "proxy_url"); err != nil {
		return view, err
	}
	if err := configValue(desktop, "proxy_url", &view.Editable.Desktop.ProxyURL); err != nil {
		return view, err
	}
	if !validDesktopProxy(view.Editable.Desktop.ProxyURL) {
		return view, invalidConfig("desktop.proxy_url")
	}
	view.Revision = configRevision(raw)
	view.SafeSummary.ListenHost, view.SafeSummary.SchedulesDisabled = host, !view.Editable.Schedule.CheckinEnabled
	return view, nil
}

func parseDesktopListen(listen string) (string, int, error) {
	host, port, err := net.SplitHostPort(listen)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host != "127.0.0.1" && host != "0.0.0.0" || n < 1024 || n > 65535 || strconv.Itoa(n) != port {
		return "", 0, invalidConfig("listen")
	}
	return host, n, nil
}

func validCheckinHours(hours []int) bool {
	if len(hours) < 1 || len(hours) > 24 {
		return false
	}
	seen := make(map[int]bool, len(hours))
	for _, hour := range hours {
		if hour < 0 || hour > 23 || seen[hour] {
			return false
		}
		seen[hour] = true
	}
	return true
}

func validDesktopProxy(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return false
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func mergeDesktopPatch(doc map[string]json.RawMessage, raw []byte) error {
	if len(raw) > 16*1024 {
		return ErrDesktopConfigInvalid
	}
	patch, err := configObject(raw)
	if err != nil || len(patch) == 0 {
		return ErrDesktopConfigInvalid
	}
	var newPort int
	_, hasPort := patch["port"]
	if hasPort {
		if err := configValue(patch, "port", &newPort); err != nil || newPort < 1024 || newPort > 65535 {
			return invalidConfig("port")
		}
	}
	var allowLAN bool
	_, hasLAN := patch["allow_lan"]
	if hasLAN {
		if err := configValue(patch, "allow_lan", &allowLAN); err != nil {
			return invalidConfig("allow_lan")
		}
	}
	if hasPort || hasLAN {
		var listen string
		if err := configValue(doc, "listen", &listen); err != nil {
			return err
		}
		host, port, err := parseDesktopListen(listen)
		if err != nil {
			return err
		}
		if hasPort {
			port = newPort
		}
		if hasLAN {
			if allowLAN {
				host = "0.0.0.0"
			} else {
				host = "127.0.0.1"
			}
		}
		doc["listen"], _ = json.Marshal(net.JoinHostPort(host, strconv.Itoa(port)))
	}
	for name, value := range patch {
		if name == "port" || name == "allow_lan" {
			continue
		}
		if name != "upstream" && name != "pool" && name != "session_sticky" && name != "desktop" && name != "schedule" {
			return ErrDesktopConfigInvalid
		}
		edits, err := configObject(value)
		if err != nil || len(edits) == 0 {
			return invalidConfig(name)
		}
		section, err := configSection(doc, name)
		if err != nil {
			return err
		}
		for field, edit := range edits {
			switch name + "." + field {
			case "upstream.timeout_seconds", "upstream.header_timeout_seconds", "upstream.idle_timeout_seconds":
				var n int
				if err := configValue(edits, field, &n); err != nil || n < 1 || n > 86400 {
					return invalidConfig("upstream timeout")
				}
			case "pool.max_in_flight":
				var n int
				if err := configValue(edits, field, &n); err != nil || n < 0 {
					return invalidConfig("pool.max_in_flight")
				}
			case "session_sticky.enabled":
				var enabled bool
				if err := configValue(edits, field, &enabled); err != nil {
					return err
				}
			case "session_sticky.ttl":
				var ttl string
				if err := configValue(edits, field, &ttl); err != nil {
					return err
				}
				duration, err := time.ParseDuration(ttl)
				if err != nil || duration < time.Second || duration > 720*time.Hour {
					return invalidConfig("session_sticky.ttl")
				}
			case "desktop.proxy_url":
				var proxy string
				if err := configValue(edits, field, &proxy); err != nil || !validDesktopProxy(proxy) {
					return invalidConfig("desktop.proxy_url")
				}
			case "schedule.checkin_enabled":
				var enabled bool
				if err := configValue(edits, field, &enabled); err != nil {
					return invalidConfig("schedule.checkin_enabled")
				}
			case "schedule.checkin_hours":
				var hours []int
				if err := configValue(edits, field, &hours); err != nil || !validCheckinHours(hours) {
					return invalidConfig("schedule.checkin_hours")
				}
			default:
				return ErrDesktopConfigInvalid
			}
			section[field] = edit
		}
		doc[name], _ = json.Marshal(section)
	}
	return nil
}

type desktopBackup struct {
	id       string
	size     int64
	modified time.Time
	valid    bool
}

func (s *DesktopConfigStore) backupPath(id string) string {
	return filepath.Join(s.backupDir, desktopBackupPrefix+id+".json")
}

func desktopBackupID(name string) string {
	if !strings.HasPrefix(name, desktopBackupPrefix) || !strings.HasSuffix(name, ".json") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, desktopBackupPrefix), ".json")
	raw, err := hex.DecodeString(id)
	if err != nil || len(raw) != 16 || strings.ToLower(id) != id {
		return ""
	}
	return id
}

func (s *DesktopConfigStore) reserveBackup(ctx context.Context, id string, raw []byte, revision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := privateDir(filepath.Dir(s.backupDir)); err != nil {
		return err
	}
	if err := fsutil.EnsurePrivateDir(s.backupDir); err != nil {
		return ErrDesktopConfigStorage
	}
	if err := privateDir(s.backupDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		return ErrDesktopConfigStorage
	}
	var backups []desktopBackup
	var total int64
	for _, entry := range entries {
		backupID := desktopBackupID(entry.Name())
		if backupID == "" {
			continue
		} // Never prune account files or other applications' files.
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || fsutil.PermBitsEnforced() && info.Mode().Perm() != 0o600 ||
			fsutil.CheckPrivateACL(s.backupPath(backupID)) != nil {
			return ErrDesktopConfigStorage
		}
		if info.Size() > desktopConfigByteLimit {
			return ErrDesktopConfigBudget
		}
		content, err := readPrivateConfig(s.backupPath(backupID))
		if err != nil {
			return err
		}
		_, validErr := desktopConfigProjection(content)
		backups = append(backups, desktopBackup{id: backupID, size: info.Size(), modified: info.ModTime(), valid: validErr == nil})
		total += info.Size()
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].modified.Equal(backups[j].modified) {
			return backups[i].id < backups[j].id
		}
		return backups[i].modified.Before(backups[j].modified)
	})
	latestValid := ""
	for _, backup := range backups {
		if backup.valid {
			latestValid = backup.id
		}
	}
	count := len(backups) + 1
	total += int64(len(raw))
	var remove []desktopBackup
	for _, backup := range backups {
		if count <= desktopConfigBackupLimit && total <= desktopConfigByteLimit {
			break
		}
		if backup.id == latestValid || s.active != nil && backup.id == s.active.BackupID {
			continue
		}
		remove = append(remove, backup)
		count--
		total -= backup.size
	}
	if count > desktopConfigBackupLimit || total > desktopConfigByteLimit {
		return ErrDesktopConfigBudget
	}
	// Plan the whole eviction first: an impossible budget never deletes backups.
	for _, backup := range remove {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(s.backupPath(backup.id)); err != nil {
			return ErrDesktopConfigStorage
		}
	}
	err = fsutil.WriteFileAtomicStrict(ctx, s.backupPath(id), raw, 0o600, func() error {
		if err := privateDir(s.backupDir); err != nil {
			return err
		}
		if _, err := os.Lstat(s.backupPath(id)); !os.IsNotExist(err) {
			return ErrDesktopConfigStorage
		}
		return s.checkRevision(revision)
	})
	return desktopStorageError(err)
}
