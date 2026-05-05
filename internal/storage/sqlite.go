package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"gpt-image-web/internal/domain"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string, maxOpenConns int) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	db.SetConnMaxLifetime(30 * time.Minute)

	store := &Store{db: db}
	if err := store.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) configure(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA temp_store=MEMORY",
	}
	for _, stmt := range pragmas {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('admin','user')),
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_login_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_role ON users(role)`,
		`CREATE INDEX IF NOT EXISTS idx_users_status ON users(status)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('admin','user')),
			key_hash TEXT NOT NULL UNIQUE,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			last_used_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_role ON api_keys(role)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			access_token TEXT PRIMARY KEY,
			type TEXT NOT NULL DEFAULT 'free',
			status TEXT NOT NULL DEFAULT '正常',
			quota INTEGER NOT NULL DEFAULT 0,
			image_quota_unknown INTEGER NOT NULL DEFAULT 0,
			email TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			limits_progress_json TEXT NOT NULL DEFAULT '[]',
			default_model_slug TEXT NOT NULL DEFAULT '',
			restore_at TEXT NOT NULL DEFAULT '',
			success INTEGER NOT NULL DEFAULT 0,
			fail INTEGER NOT NULL DEFAULT 0,
			last_used_at TEXT NOT NULL DEFAULT '',
			raw_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(status)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value_json TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS system_logs (
			id TEXT PRIMARY KEY,
			time TEXT NOT NULL,
			type TEXT NOT NULL,
			summary TEXT NOT NULL,
			detail_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_time ON system_logs(time)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_type ON system_logs(type)`,
		`CREATE TABLE IF NOT EXISTS image_tasks (
			owner_id TEXT NOT NULL,
			id TEXT NOT NULL,
			status TEXT NOT NULL,
			mode TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			size TEXT NOT NULL DEFAULT '',
			prompt TEXT NOT NULL DEFAULT '',
			data_json TEXT,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(owner_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_image_tasks_updated_at ON image_tasks(updated_at)`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.addColumnIfMissing(ctx, "accounts", "limits_progress_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "image_tasks", "prompt", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func (s *Store) addColumnIfMissing(ctx context.Context, table string, column string, definition string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition)
	return err
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE status != 'deleted'`).Scan(&count)
	return count, err
}

func (s *Store) CreateUser(ctx context.Context, user domain.User) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO users (id, email, name, password_hash, role, status, created_at, updated_at, last_login_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID,
		normalizeEmail(user.Email),
		user.Name,
		user.PasswordHash,
		string(user.Role),
		string(user.Status),
		formatTime(user.CreatedAt),
		formatTime(user.UpdatedAt),
		formatTimePtr(user.LastLoginAt),
	)
	return err
}

func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, email, name, password_hash, role, status, created_at, updated_at, last_login_at
		 FROM users WHERE status != 'deleted' ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []domain.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if users == nil {
		users = []domain.User{}
	}
	return users, rows.Err()
}

func (s *Store) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, email, name, password_hash, role, status, created_at, updated_at, last_login_at
		 FROM users WHERE id = ? AND status != 'deleted'`,
		id,
	)
	return scanUser(row)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, email, name, password_hash, role, status, created_at, updated_at, last_login_at
		 FROM users WHERE email = ? AND status != 'deleted'`,
		normalizeEmail(email),
	)
	return scanUser(row)
}

func (s *Store) TouchUserLogin(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ?, updated_at = ? WHERE id = ?`, formatTime(at), formatTime(at), id)
	return err
}

type UserUpdate struct {
	Email        *string
	Name         *string
	PasswordHash *string
	Role         *domain.Role
	Status       *domain.UserStatus
}

func (s *Store) UpdateUser(ctx context.Context, id string, update UserUpdate) (domain.User, error) {
	current, err := s.GetUserByID(ctx, id)
	if err != nil {
		return domain.User{}, err
	}
	if update.Email != nil {
		current.Email = normalizeEmail(*update.Email)
	}
	if update.Name != nil {
		current.Name = strings.TrimSpace(*update.Name)
	}
	if update.PasswordHash != nil {
		current.PasswordHash = *update.PasswordHash
	}
	if update.Role != nil {
		current.Role = *update.Role
	}
	if update.Status != nil {
		current.Status = *update.Status
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE users SET email = ?, name = ?, password_hash = ?, role = ?, status = ?, updated_at = ? WHERE id = ?`,
		current.Email,
		current.Name,
		current.PasswordHash,
		string(current.Role),
		string(current.Status),
		formatTime(current.UpdatedAt),
		id,
	)
	if err != nil {
		return domain.User{}, err
	}
	return current, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, item domain.APIKey) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO api_keys (id, user_id, name, role, key_hash, enabled, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID,
		item.UserID,
		item.Name,
		string(item.Role),
		item.KeyHash,
		boolInt(item.Enabled),
		formatTime(item.CreatedAt),
		formatTimePtr(item.LastUsedAt),
	)
	return err
}

func (s *Store) ListAPIKeys(ctx context.Context, userID string, role string) ([]domain.APIKey, error) {
	query := `SELECT id, user_id, name, role, key_hash, enabled, created_at, last_used_at FROM api_keys WHERE 1 = 1`
	var args []any
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	if role != "" {
		query += ` AND role = ?`
		args = append(args, role)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.APIKey
	for rows.Next() {
		item, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.APIKey{}
	}
	return items, rows.Err()
}

func (s *Store) FindAPIKeyByHash(ctx context.Context, hash string) (domain.APIKey, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, user_id, name, role, key_hash, enabled, created_at, last_used_at
		 FROM api_keys WHERE key_hash = ? AND enabled = 1`,
		hash,
	)
	return scanAPIKey(row)
}

type APIKeyUpdate struct {
	Name    *string
	Enabled *bool
	KeyHash *string
}

func (s *Store) UpdateAPIKey(ctx context.Context, id string, userID string, update APIKeyUpdate) (domain.APIKey, error) {
	current, err := s.GetAPIKey(ctx, id, userID)
	if err != nil {
		return domain.APIKey{}, err
	}
	if update.Name != nil {
		current.Name = strings.TrimSpace(*update.Name)
	}
	if update.Enabled != nil {
		current.Enabled = *update.Enabled
	}
	if update.KeyHash != nil {
		current.KeyHash = *update.KeyHash
	}
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE api_keys SET name = ?, enabled = ?, key_hash = ? WHERE id = ?`,
		current.Name,
		boolInt(current.Enabled),
		current.KeyHash,
		id,
	)
	if err != nil {
		return domain.APIKey{}, err
	}
	return current, nil
}

func (s *Store) GetAPIKey(ctx context.Context, id string, userID string) (domain.APIKey, error) {
	query := `SELECT id, user_id, name, role, key_hash, enabled, created_at, last_used_at FROM api_keys WHERE id = ?`
	args := []any{id}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	return scanAPIKey(s.db.QueryRowContext(ctx, query, args...))
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string, userID string) error {
	query := `DELETE FROM api_keys WHERE id = ?`
	args := []any{id}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, formatTime(at), id)
	return err
}

func (s *Store) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT access_token, type, status, quota, image_quota_unknown, email, user_id, limits_progress_json, default_model_slug,
		 restore_at, success, fail, last_used_at, raw_json, created_at, updated_at
		 FROM accounts ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.Account
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.Account{}
	}
	return items, rows.Err()
}

func (s *Store) UpsertAccountToken(ctx context.Context, token string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO accounts (access_token, created_at, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(access_token) DO NOTHING`,
		token,
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

func (s *Store) DeleteAccounts(ctx context.Context, tokens []string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	removed := 0
	for _, token := range tokens {
		res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE access_token = ?`, strings.TrimSpace(token))
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		removed += int(affected)
	}
	return removed, tx.Commit()
}

type AccountUpdate struct {
	Type   *string
	Status *string
	Quota  *int
}

func (s *Store) UpdateAccount(ctx context.Context, accessToken string, update AccountUpdate) (domain.Account, error) {
	current, err := s.GetAccount(ctx, accessToken)
	if err != nil {
		return domain.Account{}, err
	}
	if update.Type != nil {
		current.Type = strings.TrimSpace(*update.Type)
	}
	if update.Status != nil {
		current.Status = strings.TrimSpace(*update.Status)
	}
	if update.Quota != nil {
		current.Quota = *update.Quota
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE accounts SET type = ?, status = ?, quota = ?, updated_at = ? WHERE access_token = ?`,
		current.Type,
		current.Status,
		current.Quota,
		formatTime(current.UpdatedAt),
		accessToken,
	)
	if err != nil {
		return domain.Account{}, err
	}
	return current, nil
}

func (s *Store) GetAccount(ctx context.Context, accessToken string) (domain.Account, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT access_token, type, status, quota, image_quota_unknown, email, user_id, limits_progress_json, default_model_slug,
		 restore_at, success, fail, last_used_at, raw_json, created_at, updated_at
		 FROM accounts WHERE access_token = ?`,
		accessToken,
	)
	return scanAccount(row)
}

func (s *Store) UpdateAccountRemoteInfo(ctx context.Context, accessToken string, info domain.Account) (domain.Account, error) {
	current, err := s.GetAccount(ctx, accessToken)
	if err != nil {
		return domain.Account{}, err
	}
	if info.Type != "" {
		current.Type = info.Type
	}
	if info.Status != "" {
		current.Status = info.Status
	}
	current.Quota = info.Quota
	current.ImageQuotaUnknown = info.ImageQuotaUnknown
	current.Email = info.Email
	current.UserID = info.UserID
	if len(info.LimitsProgress) > 0 {
		current.LimitsProgress = info.LimitsProgress
	}
	current.DefaultModelSlug = info.DefaultModelSlug
	current.RestoreAt = info.RestoreAt
	if len(info.RawJSON) > 0 {
		current.RawJSON = info.RawJSON
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE accounts
		 SET type = ?, status = ?, quota = ?, image_quota_unknown = ?, email = ?, user_id = ?,
		     limits_progress_json = ?, default_model_slug = ?, restore_at = ?, raw_json = ?, updated_at = ?
		 WHERE access_token = ?`,
		current.Type,
		current.Status,
		current.Quota,
		boolInt(current.ImageQuotaUnknown),
		current.Email,
		current.UserID,
		string(defaultJSON(current.LimitsProgress, `[]`)),
		current.DefaultModelSlug,
		current.RestoreAt,
		string(defaultJSON(current.RawJSON, `{}`)),
		formatTime(current.UpdatedAt),
		accessToken,
	)
	if err != nil {
		return domain.Account{}, err
	}
	return current, nil
}

func (s *Store) MarkAccountUsed(ctx context.Context, accessToken string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE access_token = ?`,
		formatTime(now),
		formatTime(now),
		accessToken,
	)
	return err
}

func (s *Store) MarkImageResult(ctx context.Context, accessToken string, success bool) (domain.Account, error) {
	current, err := s.GetAccount(ctx, accessToken)
	if err != nil {
		return domain.Account{}, err
	}
	if success {
		current.Success++
		if !current.ImageQuotaUnknown && current.Quota > 0 {
			current.Quota--
		}
		if !current.ImageQuotaUnknown && current.Quota == 0 {
			current.Status = "限流"
		} else if current.Status == "限流" {
			current.Status = "正常"
		}
	} else {
		current.Fail++
	}
	now := time.Now().UTC()
	current.LastUsedAt = formatTime(now)
	current.UpdatedAt = now
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE accounts
		 SET status = ?, quota = ?, success = ?, fail = ?, last_used_at = ?, updated_at = ?
		 WHERE access_token = ?`,
		current.Status,
		current.Quota,
		current.Success,
		current.Fail,
		current.LastUsedAt,
		formatTime(current.UpdatedAt),
		accessToken,
	)
	if err != nil {
		return domain.Account{}, err
	}
	return current, nil
}

func (s *Store) GetSettings(ctx context.Context) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value_json FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := defaultSettings()
	for rows.Next() {
		var key string
		var payload string
		if err := rows.Scan(&key, &payload); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal([]byte(payload), &value); err == nil {
			result[key] = value
		}
	}
	return result, rows.Err()
}

func (s *Store) SaveSettings(ctx context.Context, settings map[string]any) (map[string]any, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	for key, value := range settings {
		if key == "" || key == "auth-key" {
			continue
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
			key,
			string(payload),
			now,
		)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetSettings(ctx)
}

func (s *Store) AddLog(ctx context.Context, item domain.SystemLog) error {
	if len(item.Detail) == 0 {
		item.Detail = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO system_logs (id, time, type, summary, detail_json) VALUES (?, ?, ?, ?, ?)`,
		item.ID,
		formatTime(item.Time),
		item.Type,
		item.Summary,
		string(item.Detail),
	)
	return err
}

func (s *Store) ListLogs(ctx context.Context, logType string, ids []string, includeDetail bool) ([]domain.SystemLog, error) {
	detailExpr := `NULL`
	if includeDetail {
		detailExpr = `detail_json`
	}
	query := `SELECT id, time, type, summary, ` + detailExpr + ` FROM system_logs`
	var args []any
	if logType != "" {
		query += ` WHERE type = ?`
		args = append(args, logType)
	}
	if len(ids) > 0 {
		if logType == "" {
			query += ` WHERE`
		} else {
			query += ` AND`
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
		query += ` id IN (` + placeholders + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query += ` ORDER BY time DESC LIMIT 1000`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.SystemLog
	for rows.Next() {
		var item domain.SystemLog
		var at string
		var detail sql.NullString
		if err := rows.Scan(&item.ID, &at, &item.Type, &item.Summary, &detail); err != nil {
			return nil, err
		}
		item.Time = parseTime(at)
		if detail.Valid && detail.String != "" {
			item.Detail = json.RawMessage(detail.String)
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.SystemLog{}
	}
	return items, rows.Err()
}

func (s *Store) DeleteLogs(ctx context.Context, ids []string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	removed := 0
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `DELETE FROM system_logs WHERE id = ?`, strings.TrimSpace(id))
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		removed += int(affected)
	}
	return removed, tx.Commit()
}

func (s *Store) CreateImageTask(ctx context.Context, task domain.ImageTask) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO image_tasks (owner_id, id, status, mode, model, size, prompt, data_json, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.OwnerID,
		task.ID,
		task.Status,
		task.Mode,
		task.Model,
		task.Size,
		task.Prompt,
		nullJSON(task.Data),
		task.Error,
		formatTime(task.CreatedAt),
		formatTime(task.UpdatedAt),
	)
	return err
}

func (s *Store) ListImageTasks(ctx context.Context, ownerID string, ids []string, includeData bool) ([]domain.ImageTask, error) {
	dataExpr := `NULL`
	if includeData {
		dataExpr = `data_json`
	}
	query := `SELECT owner_id, id, status, mode, model, size, prompt, ` + dataExpr + `, error, created_at, updated_at FROM image_tasks WHERE owner_id = ?`
	args := []any{ownerID}
	if len(ids) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
		query += ` AND id IN (` + placeholders + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query += ` ORDER BY updated_at DESC LIMIT 200`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.ImageTask
	for rows.Next() {
		item, err := scanImageTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.ImageTask{}
	}
	return items, rows.Err()
}

func (s *Store) DeleteImageTasks(ctx context.Context, ownerID string, ids []string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	removed := 0
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `DELETE FROM image_tasks WHERE owner_id = ? AND id = ?`, ownerID, strings.TrimSpace(id))
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		removed += int(affected)
	}
	return removed, tx.Commit()
}

func (s *Store) UpdateImageTask(ctx context.Context, ownerID string, id string, status string, data json.RawMessage, taskErr string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE image_tasks SET status = ?, data_json = ?, error = ?, updated_at = ? WHERE owner_id = ? AND id = ?`,
		status,
		nullJSON(data),
		taskErr,
		formatTime(time.Now().UTC()),
		ownerID,
		id,
	)
	return err
}

func defaultSettings() map[string]any {
	return map[string]any{
		"proxy":                             "",
		"base_url":                          "",
		"global_system_prompt":              "",
		"sensitive_words":                   []any{},
		"refresh_account_interval_minute":   5,
		"image_retention_days":              30,
		"image_poll_timeout_secs":           120,
		"image_account_concurrency":         1,
		"auto_remove_invalid_accounts":      false,
		"auto_remove_rate_limited_accounts": false,
		"log_levels":                        []any{},
		"ai_review":                         map[string]any{"enabled": false, "base_url": "", "api_key": "", "model": "", "prompt": ""},
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (domain.User, error) {
	var user domain.User
	var role string
	var status string
	var createdAt string
	var updatedAt string
	var lastLogin sql.NullString
	err := row.Scan(&user.ID, &user.Email, &user.Name, &user.PasswordHash, &role, &status, &createdAt, &updatedAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	user.Role = domain.Role(role)
	user.Status = domain.UserStatus(status)
	user.CreatedAt = parseTime(createdAt)
	user.UpdatedAt = parseTime(updatedAt)
	if lastLogin.Valid && lastLogin.String != "" {
		value := parseTime(lastLogin.String)
		user.LastLoginAt = &value
	}
	return user, nil
}

func scanAPIKey(row rowScanner) (domain.APIKey, error) {
	var item domain.APIKey
	var role string
	var enabled int
	var createdAt string
	var lastUsed sql.NullString
	err := row.Scan(&item.ID, &item.UserID, &item.Name, &role, &item.KeyHash, &enabled, &createdAt, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.APIKey{}, ErrNotFound
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	item.Role = domain.Role(role)
	item.Enabled = enabled == 1
	item.CreatedAt = parseTime(createdAt)
	if lastUsed.Valid && lastUsed.String != "" {
		value := parseTime(lastUsed.String)
		item.LastUsedAt = &value
	}
	return item, nil
}

func scanAccount(row rowScanner) (domain.Account, error) {
	var item domain.Account
	var imageQuotaUnknown int
	var limitsProgress string
	var raw string
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&item.AccessToken,
		&item.Type,
		&item.Status,
		&item.Quota,
		&imageQuotaUnknown,
		&item.Email,
		&item.UserID,
		&limitsProgress,
		&item.DefaultModelSlug,
		&item.RestoreAt,
		&item.Success,
		&item.Fail,
		&item.LastUsedAt,
		&raw,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Account{}, ErrNotFound
	}
	if err != nil {
		return domain.Account{}, err
	}
	item.ImageQuotaUnknown = imageQuotaUnknown == 1
	item.LimitsProgress = json.RawMessage(limitsProgress)
	item.RawJSON = json.RawMessage(raw)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func defaultJSON(value json.RawMessage, fallback string) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(fallback)
	}
	return value
}

func scanImageTask(row rowScanner) (domain.ImageTask, error) {
	var item domain.ImageTask
	var data sql.NullString
	var createdAt string
	var updatedAt string
	err := row.Scan(&item.OwnerID, &item.ID, &item.Status, &item.Mode, &item.Model, &item.Size, &item.Prompt, &data, &item.Error, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ImageTask{}, ErrNotFound
	}
	if err != nil {
		return domain.ImageTask{}, err
	}
	if data.Valid && data.String != "" {
		item.Data = json.RawMessage(data.String)
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed
	}
	return time.Now().UTC()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}
