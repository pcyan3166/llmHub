package llmhub

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var ErrBudget = errors.New("monthly project budget exhausted")
var ErrConflict = errors.New("configuration changed; reload before saving")

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	lock *os.File
}

type Key struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Prefix    string `json:"prefix"`
	CreatedAt string `json:"created_at"`
	Revoked   bool   `json:"revoked"`
}

type Attempt struct {
	ID            string   `json:"id"`
	RequestID     string   `json:"request_id"`
	ProjectID     string   `json:"project_id"`
	SceneID       string   `json:"scene_id"`
	ProfileID     string   `json:"profile_id"`
	PoolID        string   `json:"pool_id"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Status        string   `json:"status"`
	HTTPStatus    int      `json:"http_status"`
	InputTokens   int64    `json:"input_tokens"`
	OutputTokens  int64    `json:"output_tokens"`
	CostUSD       float64  `json:"cost_usd"`
	Estimated     bool     `json:"estimated"`
	QueueMS       int64    `json:"queue_ms"`
	DurationMS    int64    `json:"duration_ms"`
	CreatedAt     string   `json:"created_at"`
	PriceSnapshot *Profile `json:"price_snapshot,omitempty"`
}

type ProjectUsage struct {
	ProjectID    string  `json:"project_id"`
	Month        string  `json:"month"`
	Requests     int     `json:"attempts"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	ReservedUSD  float64 `json:"reserved_usd"`
	Errors       int     `json:"errors"`
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("database already in use by another llmHub process: %w", err)
	}
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock}
	_, err = db.Exec(`
BEGIN IMMEDIATE;
CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL, config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS config_history (version INTEGER PRIMARY KEY, created_at TEXT NOT NULL, config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS project_keys (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, hash TEXT UNIQUE NOT NULL, prefix TEXT NOT NULL, created_at TEXT NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS attempts (id TEXT PRIMARY KEY, request_id TEXT NOT NULL, project_id TEXT NOT NULL, scene_id TEXT NOT NULL, profile_id TEXT NOT NULL, pool_id TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL, month TEXT NOT NULL, created_at TEXT NOT NULL, status TEXT NOT NULL, http_status INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cost_micros INTEGER NOT NULL DEFAULT 0, reserved_micros INTEGER NOT NULL DEFAULT 0, estimated INTEGER NOT NULL DEFAULT 1, queue_ms INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS attempts_project_month ON attempts(project_id,month);
CREATE INDEX IF NOT EXISTS attempts_created ON attempts(created_at);
CREATE TABLE IF NOT EXISTS attempt_prices (attempt_id TEXT PRIMARY KEY, profile TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS monthly_usage (project_id TEXT NOT NULL, month TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cost_micros INTEGER NOT NULL DEFAULT 0, reserved_micros INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(project_id,month));
CREATE TABLE IF NOT EXISTS rate_events (id TEXT PRIMARY KEY, pool_id TEXT NOT NULL, at_ms INTEGER NOT NULL, tokens INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS rate_events_time ON rate_events(at_ms);
INSERT OR IGNORE INTO settings VALUES (1,1,'{"projects":[],"profiles":[],"scenes":[],"pools":[]}');
UPDATE monthly_usage SET cost_micros=cost_micros+reserved_micros, reserved_micros=0, errors=errors+(SELECT COUNT(*) FROM attempts WHERE attempts.project_id=monthly_usage.project_id AND attempts.month=monthly_usage.month AND status='running'), input_tokens=input_tokens+COALESCE((SELECT SUM(input_tokens) FROM attempts WHERE attempts.project_id=monthly_usage.project_id AND attempts.month=monthly_usage.month AND status='running'),0), output_tokens=output_tokens+COALESCE((SELECT SUM(output_tokens) FROM attempts WHERE attempts.project_id=monthly_usage.project_id AND attempts.month=monthly_usage.month AND status='running'),0);
UPDATE attempts SET status='interrupted', cost_micros=reserved_micros, reserved_micros=0 WHERE status='running';
COMMIT;
`)
	if err != nil {
		s.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { err := s.db.Close(); s.lock.Close(); return err }

func (s *Store) Config() (Config, int64, error) {
	var raw string
	var version int64
	var c Config
	err := s.db.QueryRow("SELECT config,version FROM settings WHERE id=1").Scan(&raw, &version)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c)
	}
	return c, version, err
}

func (s *Store) SaveConfig(c Config, version int64) (int64, error) {
	if err := c.Validate(); err != nil {
		return 0, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE settings SET config=?,version=version+1 WHERE id=1 AND version=?", string(raw), version)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrConflict
	}
	if _, err = tx.Exec("INSERT INTO config_history VALUES(?,?,?)", version+1, time.Now().UTC().Format(time.RFC3339Nano), string(raw)); err != nil {
		return 0, err
	}
	return version + 1, tx.Commit()
}

func randomID(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

func keyHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateKey(project string) (Key, string, error) {
	token := randomID("lh_")
	k := Key{ID: randomID("key_"), ProjectID: project, Prefix: token[:11], CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	_, err := s.db.Exec("INSERT INTO project_keys (id,project_id,hash,prefix,created_at) VALUES(?,?,?,?,?)", k.ID, project, keyHash(token), k.Prefix, k.CreatedAt)
	return k, token, err
}

func (s *Store) Authenticate(token string) (string, error) {
	var project string
	err := s.db.QueryRow("SELECT project_id FROM project_keys WHERE hash=? AND revoked=0", keyHash(token)).Scan(&project)
	return project, err
}

func (s *Store) Keys() ([]Key, error) {
	rows, err := s.db.Query("SELECT id,project_id,prefix,created_at,revoked FROM project_keys ORDER BY created_at DESC LIMIT 1000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		var k Key
		if err = rows.Scan(&k.ID, &k.ProjectID, &k.Prefix, &k.CreatedAt, &k.Revoked); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) RevokeKey(id string) error {
	res, err := s.db.Exec("UPDATE project_keys SET revoked=1 WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) Reserve(a Attempt, budget float64, reserve int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	month := time.Now().UTC().Format("2006-01")
	var used int64
	if _, err = tx.Exec("INSERT OR IGNORE INTO monthly_usage(project_id,month) VALUES(?,?)", a.ProjectID, month); err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT cost_micros+reserved_micros FROM monthly_usage WHERE project_id=? AND month=?", a.ProjectID, month).Scan(&used); err != nil {
		return err
	}
	if budget > 0 && used+reserve > int64(mathFloorMicros(budget)) {
		return ErrBudget
	}
	_, err = tx.Exec(`INSERT INTO attempts (id,request_id,project_id,scene_id,profile_id,pool_id,provider,model,month,created_at,status,input_tokens,output_tokens,reserved_micros,queue_ms) VALUES(?,?,?,?,?,?,?,?,?,?,'running',?,?,?,?)`, a.ID, a.RequestID, a.ProjectID, a.SceneID, a.ProfileID, a.PoolID, a.Provider, a.Model, month, time.Now().UTC().Format(time.RFC3339Nano), a.InputTokens, a.OutputTokens, reserve, a.QueueMS)
	if err != nil {
		return err
	}
	if a.PriceSnapshot != nil {
		raw, err := json.Marshal(a.PriceSnapshot)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO attempt_prices VALUES(?,?)", a.ID, string(raw)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE monthly_usage SET attempts=attempts+1,reserved_micros=reserved_micros+? WHERE project_id=? AND month=?", reserve, a.ProjectID, month); err != nil {
		return err
	}
	return tx.Commit()
}

func mathFloorMicros(v float64) float64 { return v * 1000000 }

func (s *Store) Settle(id, status string, httpStatus int, input, output, cost, duration int64, estimated bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, month string
	var reserve int64
	err = tx.QueryRow("SELECT project_id,month,reserved_micros FROM attempts WHERE id=? AND status='running'", id).Scan(&project, &month, &reserve)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	errors := 0
	if status != "success" {
		errors = 1
	}
	_, err = tx.Exec("UPDATE monthly_usage SET input_tokens=input_tokens+?,output_tokens=output_tokens+?,cost_micros=cost_micros+?,reserved_micros=reserved_micros-?,errors=errors+? WHERE project_id=? AND month=?", input, output, cost, reserve, errors, project, month)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE attempts SET status=?,http_status=?,input_tokens=?,output_tokens=?,cost_micros=?,reserved_micros=0,estimated=?,duration_ms=? WHERE id=?", status, httpStatus, input, output, cost, estimated, duration, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Recent(project string, limit int) ([]Attempt, error) {
	rows, err := s.db.Query(`SELECT id,request_id,project_id,scene_id,profile_id,pool_id,provider,model,status,http_status,input_tokens,output_tokens,cost_micros,estimated,queue_ms,duration_ms,created_at,COALESCE((SELECT profile FROM attempt_prices WHERE attempt_id=attempts.id),'null') FROM attempts WHERE (?='' OR project_id=?) ORDER BY created_at DESC LIMIT ?`, project, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		var cost int64
		var snapshot string
		if err = rows.Scan(&a.ID, &a.RequestID, &a.ProjectID, &a.SceneID, &a.ProfileID, &a.PoolID, &a.Provider, &a.Model, &a.Status, &a.HTTPStatus, &a.InputTokens, &a.OutputTokens, &cost, &a.Estimated, &a.QueueMS, &a.DurationMS, &a.CreatedAt, &snapshot); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(snapshot), &a.PriceSnapshot); err != nil {
			return nil, err
		}
		a.CostUSD = float64(cost) / 1e6
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Usage(project, month string) ([]ProjectUsage, error) {
	rows, err := s.db.Query(`SELECT project_id,attempts,input_tokens,output_tokens,cost_micros,reserved_micros,errors FROM monthly_usage WHERE month=? AND (?='' OR project_id=?) ORDER BY project_id`, month, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectUsage{}
	for rows.Next() {
		var u ProjectUsage
		var cost, reserved int64
		u.Month = month
		if err = rows.Scan(&u.ProjectID, &u.Requests, &u.InputTokens, &u.OutputTokens, &cost, &reserved, &u.Errors); err != nil {
			return nil, err
		}
		u.CostUSD = float64(cost) / 1e6
		u.ReservedUSD = float64(reserved) / 1e6
		out = append(out, u)
	}
	return out, rows.Err()
}

type UsageBreakdown struct {
	ProjectID         string  `json:"project_id"`
	SceneID           string  `json:"scene_id"`
	ProfileID         string  `json:"profile_id"`
	Provider          string  `json:"provider"`
	Model             string  `json:"model"`
	Attempts          int     `json:"attempts"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	EstimatedAttempts int     `json:"estimated_attempts"`
}

func (s *Store) Breakdown(project, month string) ([]UsageBreakdown, error) {
	rows, err := s.db.Query(`SELECT project_id,scene_id,profile_id,provider,model,COUNT(*),SUM(input_tokens),SUM(output_tokens),SUM(cost_micros),SUM(estimated) FROM attempts WHERE month=? AND (?='' OR project_id=?) AND status!='running' GROUP BY project_id,scene_id,profile_id,provider,model ORDER BY SUM(cost_micros) DESC LIMIT 1000`, month, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageBreakdown{}
	for rows.Next() {
		var b UsageBreakdown
		var micros int64
		if err := rows.Scan(&b.ProjectID, &b.SceneID, &b.ProfileID, &b.Provider, &b.Model, &b.Attempts, &b.InputTokens, &b.OutputTokens, &micros, &b.EstimatedAttempts); err != nil {
			return nil, err
		}
		b.CostUSD = float64(micros) / 1e6
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Dispatch(id, pool string, at time.Time, tokens int64) error {
	_, err := s.db.Exec("INSERT INTO rate_events VALUES(?,?,?,?)", id, pool, at.UnixMilli(), tokens)
	return err
}

func (s *Store) UpdateRate(id string, tokens int64) error {
	_, err := s.db.Exec("UPDATE rate_events SET tokens=? WHERE id=?", tokens, id)
	return err
}

func (s *Store) RateEvents(since time.Time) (map[string][]*rateEvent, error) {
	rows, err := s.db.Query("SELECT id,pool_id,at_ms,tokens FROM rate_events WHERE at_ms>=? ORDER BY at_ms", since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]*rateEvent{}
	for rows.Next() {
		var e rateEvent
		var pool string
		var ms int64
		if err = rows.Scan(&e.id, &pool, &ms, &e.tokens); err != nil {
			return nil, err
		}
		e.at = time.UnixMilli(ms)
		out[pool] = append(out[pool], &e)
	}
	return out, rows.Err()
}

// Monthly aggregates survive pruning so retention never resets a project's budget.
func (s *Store) PruneRates() error {
	_, err := s.db.Exec("DELETE FROM rate_events WHERE at_ms<?", time.Now().Add(-2*time.Minute).UnixMilli())
	if err == nil {
		_, err = s.db.Exec("DELETE FROM attempts WHERE status!='running' AND created_at<?", time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano))
	}
	if err == nil {
		_, err = s.db.Exec("DELETE FROM attempt_prices WHERE attempt_id NOT IN (SELECT id FROM attempts)")
	}
	return err
}
