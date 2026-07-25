package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	"tgworkbench/internal/domain"
	"tgworkbench/internal/id"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, phone TEXT NOT NULL, api_id INTEGER NOT NULL,
  api_hash BLOB NOT NULL, status TEXT NOT NULL, username TEXT NOT NULL DEFAULT '', user_id INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '', connected_at TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS routes (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, account_id TEXT NOT NULL, sources_json TEXT NOT NULL,
  targets_json TEXT NOT NULL, mode TEXT NOT NULL, review_mode TEXT NOT NULL, enabled INTEGER NOT NULL,
  sync_edits INTEGER NOT NULL, sync_deletes INTEGER NOT NULL, sync_reactions INTEGER NOT NULL,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS rules (
  id TEXT PRIMARY KEY, route_id TEXT NOT NULL, name TEXT NOT NULL, sort_order INTEGER NOT NULL,
  kind TEXT NOT NULL, enabled INTEGER NOT NULL, pattern TEXT NOT NULL, replacement TEXT NOT NULL,
  message_type TEXT NOT NULL, case_sensitive INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS rules_route_order ON rules(route_id, sort_order);
CREATE TABLE IF NOT EXISTS reviews (
  id TEXT PRIMARY KEY, route_id TEXT NOT NULL, route_name TEXT NOT NULL, source_chat_id INTEGER NOT NULL,
  source_title TEXT NOT NULL, sender_name TEXT NOT NULL, message_type TEXT NOT NULL,
  original_text TEXT NOT NULL, final_text TEXT NOT NULL, status TEXT NOT NULL, reason TEXT NOT NULL,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS reviews_status_created ON reviews(status, created_at DESC);
CREATE TABLE IF NOT EXISTS activities (
  id TEXT PRIMARY KEY, level TEXT NOT NULL, category TEXT NOT NULL, message TEXT NOT NULL,
  route_id TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS activities_created ON activities(created_at DESC);
CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY CHECK(id=1), value_json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS message_map (
  route_id TEXT NOT NULL, source_chat_id INTEGER NOT NULL, source_message_id INTEGER NOT NULL,
  target_chat_id INTEGER NOT NULL, target_message_id INTEGER NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY(route_id, source_chat_id, source_message_id, target_chat_id)
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM settings").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		settings := domain.Settings{ListenAddress: "127.0.0.1:8765", RetentionDays: 30, MediaCacheMB: 2048, OpenBrowser: true}
		if err := s.SaveSettings(settings); err != nil {
			return err
		}
		return s.AddActivity(domain.Activity{Level: "info", Category: "system", Message: "工作台已初始化"})
	}
	return nil
}

func encode(value any) (string, error) {
	b, err := json.Marshal(value)
	return string(b), err
}

func decode(value string, target any) error { return json.Unmarshal([]byte(value), target) }

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

func (s *Store) SaveAccount(input domain.AccountInput, encryptedHash []byte) (domain.Account, error) {
	now := nowText()
	a := domain.Account{ID: id.New(), Name: input.Name, Phone: input.Phone, APIID: input.APIID, HasAPIHash: len(encryptedHash) > 0, Status: "disconnected", CreatedAt: parseTime(now)}
	_, err := s.db.Exec(`INSERT INTO accounts(id,name,phone,api_id,api_hash,status,created_at) VALUES(?,?,?,?,?,?,?)`, a.ID, a.Name, a.Phone, a.APIID, encryptedHash, a.Status, now)
	return a, err
}

func (s *Store) ListAccounts() ([]domain.Account, error) {
	rows, err := s.db.Query(`SELECT id,name,phone,api_id,length(api_hash)>0,status,username,user_id,last_error,connected_at,created_at FROM accounts ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Account, 0)
	for rows.Next() {
		var a domain.Account
		var connected, created string
		if err := rows.Scan(&a.ID, &a.Name, &a.Phone, &a.APIID, &a.HasAPIHash, &a.Status, &a.Username, &a.UserID, &a.LastError, &connected, &created); err != nil {
			return nil, err
		}
		a.ConnectedAt, a.CreatedAt = parseTime(connected), parseTime(created)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) AccountCredentials(accountID string) (domain.Account, []byte, error) {
	var a domain.Account
	var encrypted []byte
	var connected, created string
	err := s.db.QueryRow(`SELECT id,name,phone,api_id,api_hash,status,username,user_id,last_error,connected_at,created_at FROM accounts WHERE id=?`, accountID).
		Scan(&a.ID, &a.Name, &a.Phone, &a.APIID, &encrypted, &a.Status, &a.Username, &a.UserID, &a.LastError, &connected, &created)
	a.HasAPIHash = len(encrypted) > 0
	a.ConnectedAt, a.CreatedAt = parseTime(connected), parseTime(created)
	return a, encrypted, err
}

func (s *Store) UpdateAccountStatus(accountID, status, username, lastError string, userID int64) error {
	connected := ""
	if status == "connected" {
		connected = nowText()
	}
	_, err := s.db.Exec(`UPDATE accounts SET status=?,username=CASE WHEN ?<>'' THEN ? ELSE username END,user_id=CASE WHEN ?>0 THEN ? ELSE user_id END,last_error=?,connected_at=CASE WHEN ?<>'' THEN ? ELSE connected_at END WHERE id=?`, status, username, username, userID, userID, lastError, connected, connected, accountID)
	return err
}

func (s *Store) DeleteAccount(accountID string) error {
	_, err := s.db.Exec(`DELETE FROM accounts WHERE id=?`, accountID)
	return err
}

func (s *Store) SaveRoute(route domain.Route) (domain.Route, error) {
	now := nowText()
	if route.ID == "" {
		route.ID, route.CreatedAt = id.New(), parseTime(now)
	}
	route.UpdatedAt = parseTime(now)
	sources, err := encode(route.Sources)
	if err != nil {
		return route, err
	}
	targets, err := encode(route.Targets)
	if err != nil {
		return route, err
	}
	_, err = s.db.Exec(`INSERT INTO routes(id,name,account_id,sources_json,targets_json,mode,review_mode,enabled,sync_edits,sync_deletes,sync_reactions,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,account_id=excluded.account_id,sources_json=excluded.sources_json,targets_json=excluded.targets_json,mode=excluded.mode,review_mode=excluded.review_mode,enabled=excluded.enabled,sync_edits=excluded.sync_edits,sync_deletes=excluded.sync_deletes,sync_reactions=excluded.sync_reactions,updated_at=excluded.updated_at`,
		route.ID, route.Name, route.AccountID, sources, targets, route.Mode, route.ReviewMode, route.Enabled, route.SyncEdits, route.SyncDeletes, route.SyncReactions, route.CreatedAt.UTC().Format(time.RFC3339Nano), now)
	return route, err
}

func (s *Store) ListRoutes() ([]domain.Route, error) {
	rows, err := s.db.Query(`SELECT id,name,account_id,sources_json,targets_json,mode,review_mode,enabled,sync_edits,sync_deletes,sync_reactions,created_at,updated_at FROM routes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Route, 0)
	for rows.Next() {
		var r domain.Route
		var sources, targets, created, updated string
		if err := rows.Scan(&r.ID, &r.Name, &r.AccountID, &sources, &targets, &r.Mode, &r.ReviewMode, &r.Enabled, &r.SyncEdits, &r.SyncDeletes, &r.SyncReactions, &created, &updated); err != nil {
			return nil, err
		}
		if err := decode(sources, &r.Sources); err != nil {
			return nil, err
		}
		if err := decode(targets, &r.Targets); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) Route(routeID string) (domain.Route, error) {
	routes, err := s.ListRoutes()
	if err != nil {
		return domain.Route{}, err
	}
	for _, route := range routes {
		if route.ID == routeID {
			return route, nil
		}
	}
	return domain.Route{}, sql.ErrNoRows
}

func (s *Store) DeleteRoute(routeID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM rules WHERE route_id=?`, routeID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM routes WHERE id=?`, routeID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveRule(rule domain.Rule) (domain.Rule, error) {
	now := nowText()
	if rule.ID == "" {
		rule.ID, rule.CreatedAt = id.New(), parseTime(now)
	}
	rule.UpdatedAt = parseTime(now)
	_, err := s.db.Exec(`INSERT INTO rules(id,route_id,name,sort_order,kind,enabled,pattern,replacement,message_type,case_sensitive,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET route_id=excluded.route_id,name=excluded.name,sort_order=excluded.sort_order,kind=excluded.kind,enabled=excluded.enabled,pattern=excluded.pattern,replacement=excluded.replacement,message_type=excluded.message_type,case_sensitive=excluded.case_sensitive,updated_at=excluded.updated_at`, rule.ID, rule.RouteID, rule.Name, rule.Order, rule.Kind, rule.Enabled, rule.Pattern, rule.Replacement, rule.MessageType, rule.CaseSensitive, rule.CreatedAt.UTC().Format(time.RFC3339Nano), now)
	return rule, err
}

func (s *Store) ListRules(routeID string) ([]domain.Rule, error) {
	query := `SELECT id,route_id,name,sort_order,kind,enabled,pattern,replacement,message_type,case_sensitive,created_at,updated_at FROM rules`
	args := []any{}
	if routeID != "" {
		query += ` WHERE route_id=?`
		args = append(args, routeID)
	}
	query += ` ORDER BY route_id,sort_order`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Rule, 0)
	for rows.Next() {
		var r domain.Rule
		var created, updated string
		if err := rows.Scan(&r.ID, &r.RouteID, &r.Name, &r.Order, &r.Kind, &r.Enabled, &r.Pattern, &r.Replacement, &r.MessageType, &r.CaseSensitive, &created, &updated); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) DeleteRule(ruleID string) error {
	_, err := s.db.Exec(`DELETE FROM rules WHERE id=?`, ruleID)
	return err
}

func (s *Store) AddReview(item domain.ReviewItem) (domain.ReviewItem, error) {
	now := nowText()
	if item.ID == "" {
		item.ID = id.New()
	}
	if item.Status == "" {
		item.Status = "pending"
	}
	item.CreatedAt, item.UpdatedAt = parseTime(now), parseTime(now)
	_, err := s.db.Exec(`INSERT INTO reviews(id,route_id,route_name,source_chat_id,source_title,sender_name,message_type,original_text,final_text,status,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.RouteID, item.RouteName, item.SourceChatID, item.SourceTitle, item.SenderName, item.MessageType, item.OriginalText, item.FinalText, item.Status, item.Reason, now, now)
	return item, err
}

func (s *Store) ListReviews(status string) ([]domain.ReviewItem, error) {
	query := `SELECT id,route_id,route_name,source_chat_id,source_title,sender_name,message_type,original_text,final_text,status,reason,created_at,updated_at FROM reviews`
	args := []any{}
	if status != "" {
		query += ` WHERE status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ReviewItem, 0)
	for rows.Next() {
		var i domain.ReviewItem
		var created, updated string
		if err := rows.Scan(&i.ID, &i.RouteID, &i.RouteName, &i.SourceChatID, &i.SourceTitle, &i.SenderName, &i.MessageType, &i.OriginalText, &i.FinalText, &i.Status, &i.Reason, &created, &updated); err != nil {
			return nil, err
		}
		i.CreatedAt, i.UpdatedAt = parseTime(created), parseTime(updated)
		result = append(result, i)
	}
	return result, rows.Err()
}

func (s *Store) Review(itemID string) (domain.ReviewItem, error) {
	var item domain.ReviewItem
	var created, updated string
	err := s.db.QueryRow(`SELECT id,route_id,route_name,source_chat_id,source_title,sender_name,message_type,original_text,final_text,status,reason,created_at,updated_at FROM reviews WHERE id=?`, itemID).
		Scan(&item.ID, &item.RouteID, &item.RouteName, &item.SourceChatID, &item.SourceTitle, &item.SenderName, &item.MessageType, &item.OriginalText, &item.FinalText, &item.Status, &item.Reason, &created, &updated)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, err
}

func (s *Store) UpdateReview(itemID, status, finalText string) error {
	_, err := s.db.Exec(`UPDATE reviews SET status=?,final_text=?,updated_at=? WHERE id=?`, status, finalText, nowText(), itemID)
	return err
}

func (s *Store) AddActivity(a domain.Activity) error {
	if a.ID == "" {
		a.ID = id.New()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`INSERT INTO activities(id,level,category,message,route_id,created_at) VALUES(?,?,?,?,?,?)`, a.ID, a.Level, a.Category, a.Message, a.RouteID, a.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListActivities(limit int) ([]domain.Activity, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id,level,category,message,route_id,created_at FROM activities ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Activity, 0)
	for rows.Next() {
		var a domain.Activity
		var created string
		if err := rows.Scan(&a.ID, &a.Level, &a.Category, &a.Message, &a.RouteID, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) Settings() (domain.Settings, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value_json FROM settings WHERE id=1`).Scan(&raw)
	var value domain.Settings
	if err == nil {
		err = decode(raw, &value)
	}
	return value, err
}
func (s *Store) SaveSettings(value domain.Settings) error {
	raw, err := encode(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings(id,value_json) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value_json=excluded.value_json`, raw)
	return err
}

func (s *Store) SaveMessageMapping(mapping domain.MessageMapping) error {
	_, err := s.db.Exec(`INSERT INTO message_map(route_id,source_chat_id,source_message_id,target_chat_id,target_message_id,created_at)
VALUES(?,?,?,?,?,?) ON CONFLICT(route_id,source_chat_id,source_message_id,target_chat_id)
DO UPDATE SET target_message_id=excluded.target_message_id,created_at=excluded.created_at`,
		mapping.RouteID, mapping.SourceChatID, mapping.SourceMessageID,
		mapping.TargetChatID, mapping.TargetMessageID, nowText())
	return err
}

func (s *Store) MessageMappings(sourceChatID int64, sourceMessageID int) ([]domain.MessageMapping, error) {
	query := `SELECT route_id,source_chat_id,source_message_id,target_chat_id,target_message_id FROM message_map WHERE source_message_id=?`
	args := []any{sourceMessageID}
	if sourceChatID != 0 {
		query += ` AND source_chat_id=?`
		args = append(args, sourceChatID)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.MessageMapping, 0)
	for rows.Next() {
		var mapping domain.MessageMapping
		if err := rows.Scan(&mapping.RouteID, &mapping.SourceChatID, &mapping.SourceMessageID, &mapping.TargetChatID, &mapping.TargetMessageID); err != nil {
			return nil, err
		}
		result = append(result, mapping)
	}
	return result, rows.Err()
}

func (s *Store) DeleteMessageMapping(mapping domain.MessageMapping) error {
	_, err := s.db.Exec(`DELETE FROM message_map WHERE route_id=? AND source_chat_id=? AND source_message_id=? AND target_chat_id=?`,
		mapping.RouteID, mapping.SourceChatID, mapping.SourceMessageID, mapping.TargetChatID)
	return err
}

func (s *Store) Dashboard() (domain.Dashboard, error) {
	var d domain.Dashboard
	if err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(enabled),0) FROM routes`).Scan(&d.TotalRoutes, &d.RunningRoutes); err != nil {
		return d, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM reviews WHERE status='pending'`).Scan(&d.PendingReview); err != nil {
		return d, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM activities WHERE category='sent' AND created_at>=date('now')`).Scan(&d.SentToday); err != nil {
		return d, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM activities WHERE level='error' AND created_at>=date('now')`).Scan(&d.FailedToday); err != nil {
		return d, err
	}
	var err error
	d.Accounts, err = s.ListAccounts()
	if err != nil {
		return d, err
	}
	d.Recent, err = s.ListActivities(8)
	return d, err
}
