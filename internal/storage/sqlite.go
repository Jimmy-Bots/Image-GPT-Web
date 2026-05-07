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

func (s *Store) BackupDatabase(ctx context.Context, destPath string) error {
	if strings.TrimSpace(destPath) == "" {
		return fmt.Errorf("destination path is required")
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, destPath); err != nil {
		return err
	}
	return nil
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
			quota_unlimited INTEGER NOT NULL DEFAULT 0,
			permanent_quota INTEGER NOT NULL DEFAULT 0,
			temporary_quota INTEGER NOT NULL DEFAULT 0,
			temporary_quota_date TEXT NOT NULL DEFAULT '',
			daily_temporary_quota INTEGER NOT NULL DEFAULT 0,
			quota_used_total INTEGER NOT NULL DEFAULT 0,
			quota_used_today INTEGER NOT NULL DEFAULT 0,
			quota_used_date TEXT NOT NULL DEFAULT '',
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
			password TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'free',
			status TEXT NOT NULL DEFAULT '正常',
			quota INTEGER NOT NULL DEFAULT 0,
			max_concurrency INTEGER NOT NULL DEFAULT 0,
			image_quota_unknown INTEGER NOT NULL DEFAULT 0,
			email TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			limits_progress_json TEXT NOT NULL DEFAULT '[]',
			default_model_slug TEXT NOT NULL DEFAULT '',
			restore_at TEXT NOT NULL DEFAULT '',
			recovery_state TEXT NOT NULL DEFAULT '',
			recovery_error TEXT NOT NULL DEFAULT '',
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
			detail_json TEXT NOT NULL DEFAULT '{}',
			actor_id TEXT NOT NULL DEFAULT '',
			subject_id TEXT NOT NULL DEFAULT '',
			task_id TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_time ON system_logs(time)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_type ON system_logs(type)`,
		`CREATE TABLE IF NOT EXISTS image_tasks (
			owner_id TEXT NOT NULL,
			id TEXT NOT NULL,
			status TEXT NOT NULL,
			phase TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			size TEXT NOT NULL DEFAULT '',
			prompt TEXT NOT NULL DEFAULT '',
			requested_count INTEGER NOT NULL DEFAULT 1,
			reserved_quota_json TEXT NOT NULL DEFAULT '{}',
			data_json TEXT,
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			deleted_by TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(owner_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_image_tasks_updated_at ON image_tasks(updated_at)`,
		`CREATE TABLE IF NOT EXISTS task_events (
			owner_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			id TEXT NOT NULL,
			time TEXT NOT NULL,
			type TEXT NOT NULL,
			summary TEXT NOT NULL,
			detail_json TEXT NOT NULL DEFAULT '{}',
			PRIMARY KEY(owner_id, task_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_events_task_time ON task_events(owner_id, task_id, time)`,
		`CREATE INDEX IF NOT EXISTS idx_task_events_time ON task_events(owner_id, time)`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureSingleAPIKeyPerUser(ctx); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "accounts", "limits_progress_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "accounts", "password", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "accounts", "max_concurrency", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "accounts", "recovery_state", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "accounts", "recovery_error", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	for _, column := range []string{"actor_id", "subject_id", "task_id", "endpoint", "status"} {
		if err := s.addColumnIfMissing(ctx, "system_logs", column, "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if err := s.ensureLogIndexes(ctx); err != nil {
		return err
	}
	if err := s.backfillLogIndexColumns(ctx); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "quota_unlimited", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "permanent_quota", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "temporary_quota", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "temporary_quota_date", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "daily_temporary_quota", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "quota_used_total", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "quota_used_today", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "users", "quota_used_date", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "image_tasks", "prompt", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "image_tasks", "requested_count", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "image_tasks", "phase", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "image_tasks", "reserved_quota_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "image_tasks", "deleted_at", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "image_tasks", "deleted_by", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureTaskEventsRetainAfterTaskDelete(ctx); err != nil {
		return err
	}
	if err := s.backfillTaskEventsToSystemLogs(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureTaskEventsRetainAfterTaskDelete(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_list(task_events)`)
	if err != nil {
		return err
	}
	hasImageTaskFK := false
	for rows.Next() {
		var id int
		var seq int
		var table string
		var from string
		var to string
		var onUpdate string
		var onDelete string
		var match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		if table == "image_tasks" {
			hasImageTaskFK = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !hasImageTaskFK {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`DROP TABLE IF EXISTS task_events_next`,
		`CREATE TABLE task_events_next (
			owner_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			id TEXT NOT NULL,
			time TEXT NOT NULL,
			type TEXT NOT NULL,
			summary TEXT NOT NULL,
			detail_json TEXT NOT NULL DEFAULT '{}',
			PRIMARY KEY(owner_id, task_id, id)
		)`,
		`INSERT OR IGNORE INTO task_events_next (owner_id, task_id, id, time, type, summary, detail_json)
			SELECT owner_id, task_id, id, time, type, summary, detail_json FROM task_events`,
		`DROP TABLE task_events`,
		`ALTER TABLE task_events_next RENAME TO task_events`,
		`CREATE INDEX IF NOT EXISTS idx_task_events_task_time ON task_events(owner_id, task_id, time)`,
		`CREATE INDEX IF NOT EXISTS idx_task_events_time ON task_events(owner_id, time)`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ensureLogIndexes(ctx context.Context) error {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_system_logs_actor_id ON system_logs(actor_id)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_subject_id ON system_logs(subject_id)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_task_id ON system_logs(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_endpoint ON system_logs(endpoint)`,
		`CREATE INDEX IF NOT EXISTS idx_system_logs_status ON system_logs(status)`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) backfillTaskEventsToSystemLogs(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT owner_id, task_id, id, time, summary, detail_json FROM task_events ORDER BY time ASC, id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ownerID string
		taskID  string
		id      string
		at      string
		summary string
		detail  string
		fields  logIndexFields
	}
	items := make([]item, 0)
	for rows.Next() {
		var item item
		if err := rows.Scan(&item.ownerID, &item.taskID, &item.id, &item.at, &item.summary, &item.detail); err != nil {
			return err
		}
		item.fields = logIndexFieldsFromRaw([]byte(item.detail))
		if item.fields.SubjectID == "" {
			item.fields.SubjectID = item.ownerID
		}
		if item.fields.TaskID == "" {
			item.fields.TaskID = item.taskID
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO system_logs (id, time, type, summary, detail_json, actor_id, subject_id, task_id, endpoint, status) VALUES (?, ?, 'task', ?, ?, ?, ?, ?, ?, ?)`,
			"task_event_"+item.id,
			item.at,
			item.summary,
			item.detail,
			item.fields.ActorID,
			item.fields.SubjectID,
			item.fields.TaskID,
			item.fields.Endpoint,
			item.fields.Status,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) backfillLogIndexColumns(ctx context.Context) error {
	type item struct {
		id     string
		fields logIndexFields
	}

	lastID := ""
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id, detail_json FROM system_logs WHERE id > ? AND actor_id = '' AND subject_id = '' AND task_id = '' AND endpoint = '' AND status = '' ORDER BY id LIMIT 5000`, lastID)
		if err != nil {
			return err
		}
		items := make([]item, 0)
		for rows.Next() {
			var id string
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item{id: id, fields: logIndexFieldsFromRaw([]byte(raw))})
			lastID = id
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(items) == 0 {
			return nil
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, item := range items {
			_, err := tx.ExecContext(ctx, `UPDATE system_logs SET actor_id = ?, subject_id = ?, task_id = ?, endpoint = ?, status = ? WHERE id = ?`,
				item.fields.ActorID,
				item.fields.SubjectID,
				item.fields.TaskID,
				item.fields.Endpoint,
				item.fields.Status,
				item.id,
			)
			if err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
}

func (s *Store) ensureSingleAPIKeyPerUser(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM api_keys
		WHERE rowid NOT IN (
			SELECT (
				SELECT candidate.rowid FROM api_keys AS candidate
				WHERE candidate.user_id = users.user_id
				ORDER BY candidate.created_at DESC, candidate.id DESC
				LIMIT 1
			)
			FROM (SELECT DISTINCT user_id FROM api_keys) AS users
		)`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_user_unique ON api_keys(user_id)`)
	return err
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
		`INSERT INTO users (id, email, name, password_hash, role, status, quota_unlimited, permanent_quota, temporary_quota, temporary_quota_date, daily_temporary_quota, quota_used_total, quota_used_today, quota_used_date, created_at, updated_at, last_login_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID,
		normalizeEmail(user.Email),
		user.Name,
		user.PasswordHash,
		string(user.Role),
		string(user.Status),
		boolInt(user.QuotaUnlimited),
		user.PermanentQuota,
		user.TemporaryQuota,
		strings.TrimSpace(user.TemporaryQuotaDate),
		user.DailyTemporaryQuota,
		user.QuotaUsedTotal,
		user.QuotaUsedToday,
		strings.TrimSpace(user.QuotaUsedDate),
		formatTime(user.CreatedAt),
		formatTime(user.UpdatedAt),
		formatTimePtr(user.LastLoginAt),
	)
	return err
}

func (s *Store) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, email, name, password_hash, role, status, quota_unlimited, permanent_quota, temporary_quota, temporary_quota_date, daily_temporary_quota, quota_used_total, quota_used_today, quota_used_date, created_at, updated_at, last_login_at
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

func (s *Store) ListUsersWithAPIKeys(ctx context.Context) ([]domain.User, error) {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for index := range users {
		users[index], err = s.refreshUserDailyTemporaryQuota(ctx, users[index], now)
		if err != nil {
			return nil, err
		}
		key, err := s.GetAPIKeyByUserID(ctx, users[index].ID)
		if err == nil {
			users[index].APIKey = &key
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return users, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, email, name, password_hash, role, status, quota_unlimited, permanent_quota, temporary_quota, temporary_quota_date, daily_temporary_quota, quota_used_total, quota_used_today, quota_used_date, created_at, updated_at, last_login_at
		 FROM users WHERE id = ? AND status != 'deleted'`,
		id,
	)
	user, err := scanUser(row)
	if err != nil {
		return domain.User{}, err
	}
	return s.refreshUserDailyTemporaryQuota(ctx, user, time.Now())
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, email, name, password_hash, role, status, quota_unlimited, permanent_quota, temporary_quota, temporary_quota_date, daily_temporary_quota, quota_used_total, quota_used_today, quota_used_date, created_at, updated_at, last_login_at
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
	Email               *string
	Name                *string
	PasswordHash        *string
	Role                *domain.Role
	Status              *domain.UserStatus
	QuotaUnlimited      *bool
	PermanentQuota      *int
	TemporaryQuota      *int
	TemporaryQuotaDate  *string
	DailyTemporaryQuota *int
	AddPermanentQuota   *int
}

func normalizeUserQuotaUsage(user *domain.User, now time.Time) {
	if user == nil {
		return
	}
	today := quotaDayString(now)
	if strings.TrimSpace(user.QuotaUsedDate) == today {
		return
	}
	user.QuotaUsedToday = 0
	user.QuotaUsedDate = today
}

func (s *Store) UpdateUser(ctx context.Context, id string, update UserUpdate) (domain.User, error) {
	current, err := s.GetUserByID(ctx, id)
	if err != nil {
		return domain.User{}, err
	}
	normalizeUserQuotaUsage(&current, time.Now())
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
	if update.QuotaUnlimited != nil {
		current.QuotaUnlimited = *update.QuotaUnlimited
	}
	if update.PermanentQuota != nil {
		current.PermanentQuota = maxInt(0, *update.PermanentQuota)
	}
	if update.AddPermanentQuota != nil {
		current.PermanentQuota = maxInt(0, current.PermanentQuota+maxInt(0, *update.AddPermanentQuota))
	}
	if update.TemporaryQuota != nil {
		current.TemporaryQuota = maxInt(0, *update.TemporaryQuota)
		if current.TemporaryQuota > 0 {
			current.TemporaryQuotaDate = quotaDayString(time.Now())
		} else {
			current.TemporaryQuotaDate = ""
		}
	}
	if update.TemporaryQuotaDate != nil && update.TemporaryQuota == nil {
		current.TemporaryQuotaDate = strings.TrimSpace(*update.TemporaryQuotaDate)
	}
	if update.DailyTemporaryQuota != nil {
		current.DailyTemporaryQuota = maxInt(0, *update.DailyTemporaryQuota)
	}
	if current.Status != domain.UserStatusActive && current.Status != domain.UserStatusDisabled && current.Status != domain.UserStatusDeleted {
		return domain.User{}, fmt.Errorf("invalid user status")
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE users SET email = ?, name = ?, password_hash = ?, role = ?, status = ?, quota_unlimited = ?, permanent_quota = ?, temporary_quota = ?, temporary_quota_date = ?, daily_temporary_quota = ?, quota_used_total = ?, quota_used_today = ?, quota_used_date = ?, updated_at = ? WHERE id = ?`,
		current.Email,
		current.Name,
		current.PasswordHash,
		string(current.Role),
		string(current.Status),
		boolInt(current.QuotaUnlimited),
		current.PermanentQuota,
		current.TemporaryQuota,
		current.TemporaryQuotaDate,
		current.DailyTemporaryQuota,
		current.QuotaUsedTotal,
		current.QuotaUsedToday,
		current.QuotaUsedDate,
		formatTime(current.UpdatedAt),
		id,
	)
	if err != nil {
		return domain.User{}, err
	}
	return current, nil
}

func quotaDayString(now time.Time) string {
	return now.Format("2006-01-02")
}

func applyDailyTemporaryQuota(user *domain.User, now time.Time) {
	if user == nil || user.QuotaUnlimited {
		return
	}
	today := quotaDayString(now)
	if strings.TrimSpace(user.TemporaryQuotaDate) == today {
		return
	}
	if configured := maxInt(0, user.DailyTemporaryQuota); configured > 0 {
		user.TemporaryQuota = configured
		user.TemporaryQuotaDate = today
		return
	}
	user.TemporaryQuota = 0
	user.TemporaryQuotaDate = ""
}

func availableUserQuota(user domain.User, now time.Time) int {
	if user.QuotaUnlimited {
		return -1
	}
	applyDailyTemporaryQuota(&user, now)
	total := maxInt(0, user.PermanentQuota)
	if strings.TrimSpace(user.TemporaryQuotaDate) == quotaDayString(now) {
		total += maxInt(0, user.TemporaryQuota)
	}
	return total
}

func (s *Store) applyDailyTemporaryQuotaIfNeeded(ctx context.Context, user *domain.User, now time.Time) error {
	if user == nil || user.QuotaUnlimited {
		return nil
	}
	beforeQuota := user.TemporaryQuota
	beforeDate := strings.TrimSpace(user.TemporaryQuotaDate)
	applyDailyTemporaryQuota(user, now)
	if user.TemporaryQuota == beforeQuota && strings.TrimSpace(user.TemporaryQuotaDate) == beforeDate {
		return nil
	}
	user.UpdatedAt = now.UTC()
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE users SET temporary_quota = ?, temporary_quota_date = ?, updated_at = ? WHERE id = ?`,
		user.TemporaryQuota,
		user.TemporaryQuotaDate,
		formatTime(user.UpdatedAt),
		user.ID,
	)
	return err
}

func (s *Store) refreshUserDailyTemporaryQuota(ctx context.Context, user domain.User, now time.Time) (domain.User, error) {
	normalizeUserQuotaUsage(&user, now)
	if err := s.applyDailyTemporaryQuotaIfNeeded(ctx, &user, now); err != nil {
		return domain.User{}, err
	}
	if _, err := s.db.ExecContext(
		ctx,
		`UPDATE users SET quota_used_today = ?, quota_used_date = ?, updated_at = ? WHERE id = ? AND (quota_used_today != ? OR quota_used_date != ?)`,
		user.QuotaUsedToday,
		user.QuotaUsedDate,
		formatTime(now.UTC()),
		user.ID,
		user.QuotaUsedToday,
		user.QuotaUsedDate,
	); err != nil {
		return domain.User{}, err
	}
	user.AvailableQuota = availableUserQuota(user, now)
	return user, nil
}

func (s *Store) ReserveUserQuota(ctx context.Context, userID string, amount int) (domain.User, domain.UserQuotaReceipt, error) {
	if amount < 1 {
		user, err := s.GetUserByID(ctx, userID)
		return user, domain.UserQuotaReceipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, domain.UserQuotaReceipt{}, err
	}
	defer tx.Rollback()
	user, err := scanUser(tx.QueryRowContext(ctx, `SELECT id, email, name, password_hash, role, status, quota_unlimited, permanent_quota, temporary_quota, temporary_quota_date, daily_temporary_quota, quota_used_total, quota_used_today, quota_used_date, created_at, updated_at, last_login_at FROM users WHERE id = ? AND status != 'deleted'`, userID))
	if err != nil {
		return domain.User{}, domain.UserQuotaReceipt{}, err
	}
	if user.QuotaUnlimited {
		if err := tx.Commit(); err != nil {
			return domain.User{}, domain.UserQuotaReceipt{}, err
		}
		return user, domain.UserQuotaReceipt{}, nil
	}
	now := time.Now()
	if err := s.applyDailyTemporaryQuotaIfNeeded(ctx, &user, now); err != nil {
		return domain.User{}, domain.UserQuotaReceipt{}, err
	}
	today := quotaDayString(now)
	temporary := 0
	if strings.TrimSpace(user.TemporaryQuotaDate) == today {
		temporary = maxInt(0, user.TemporaryQuota)
	}
	permanent := maxInt(0, user.PermanentQuota)
	available := permanent + temporary
	if available < amount {
		return domain.User{}, domain.UserQuotaReceipt{}, fmt.Errorf("quota exceeded")
	}
	receipt := domain.UserQuotaReceipt{Total: amount, TemporaryDate: today}
	consumeTemporary := minInt(temporary, amount)
	receipt.Temporary = consumeTemporary
	temporary -= consumeTemporary
	remaining := amount - consumeTemporary
	consumePermanent := minInt(permanent, remaining)
	receipt.Permanent = consumePermanent
	permanent -= consumePermanent
	temporaryDate := today
	if temporary == 0 {
		temporaryDate = ""
	}
	nowValue := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE users SET permanent_quota = ?, temporary_quota = ?, temporary_quota_date = ?, updated_at = ? WHERE id = ?`, permanent, temporary, temporaryDate, nowValue, userID); err != nil {
		return domain.User{}, domain.UserQuotaReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.User{}, domain.UserQuotaReceipt{}, err
	}
	user.PermanentQuota = permanent
	user.TemporaryQuota = temporary
	user.TemporaryQuotaDate = temporaryDate
	user.UpdatedAt = now
	user.AvailableQuota = availableUserQuota(user, now)
	return user, receipt, nil
}

func (s *Store) RefundUserQuota(ctx context.Context, userID string, receipt domain.UserQuotaReceipt) (domain.User, error) {
	if receipt.Total < 1 {
		return s.GetUserByID(ctx, userID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback()
	user, err := scanUser(tx.QueryRowContext(ctx, `SELECT id, email, name, password_hash, role, status, quota_unlimited, permanent_quota, temporary_quota, temporary_quota_date, daily_temporary_quota, quota_used_total, quota_used_today, quota_used_date, created_at, updated_at, last_login_at FROM users WHERE id = ? AND status != 'deleted'`, userID))
	if err != nil {
		return domain.User{}, err
	}
	if user.QuotaUnlimited {
		if err := tx.Commit(); err != nil {
			return domain.User{}, err
		}
		return user, nil
	}
	now := time.Now()
	if err := s.applyDailyTemporaryQuotaIfNeeded(ctx, &user, now); err != nil {
		return domain.User{}, err
	}
	permanent := maxInt(0, user.PermanentQuota) + maxInt(0, receipt.Permanent)
	temporary := maxInt(0, user.TemporaryQuota)
	temporaryDate := strings.TrimSpace(user.TemporaryQuotaDate)
	if maxInt(0, receipt.Temporary) > 0 {
		targetDate := strings.TrimSpace(receipt.TemporaryDate)
		if targetDate == "" {
			targetDate = quotaDayString(now)
		}
		if temporaryDate == "" || temporaryDate != targetDate {
			temporary = 0
		}
		temporary += maxInt(0, receipt.Temporary)
		temporaryDate = targetDate
	}
	nowValue := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE users SET permanent_quota = ?, temporary_quota = ?, temporary_quota_date = ?, updated_at = ? WHERE id = ?`, permanent, temporary, temporaryDate, nowValue, userID); err != nil {
		return domain.User{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.User{}, err
	}
	user.PermanentQuota = permanent
	user.TemporaryQuota = temporary
	user.TemporaryQuotaDate = temporaryDate
	user.UpdatedAt = now
	user.AvailableQuota = availableUserQuota(user, now)
	return user, nil
}

func (s *Store) AddUserQuotaUsage(ctx context.Context, userID string, amount int, now time.Time) (domain.User, error) {
	if amount <= 0 {
		return s.GetUserByID(ctx, userID)
	}
	current, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	normalizeUserQuotaUsage(&current, now)
	current.QuotaUsedTotal += amount
	current.QuotaUsedToday += amount
	current.QuotaUsedDate = quotaDayString(now)
	current.UpdatedAt = now.UTC()
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE users SET quota_used_total = ?, quota_used_today = ?, quota_used_date = ?, updated_at = ? WHERE id = ?`,
		current.QuotaUsedTotal,
		current.QuotaUsedToday,
		current.QuotaUsedDate,
		formatTime(current.UpdatedAt),
		userID,
	)
	if err != nil {
		return domain.User{}, err
	}
	current.AvailableQuota = availableUserQuota(current, now)
	return current, nil
}

func (s *Store) ApplyDailyTemporaryQuota(ctx context.Context, now time.Time) (int, error) {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	applied := 0
	for _, user := range users {
		if user.QuotaUnlimited || user.DailyTemporaryQuota < 1 {
			continue
		}
		beforeQuota := user.TemporaryQuota
		beforeDate := strings.TrimSpace(user.TemporaryQuotaDate)
		applyDailyTemporaryQuota(&user, now)
		if user.TemporaryQuota == beforeQuota && strings.TrimSpace(user.TemporaryQuotaDate) == beforeDate {
			continue
		}
		user.UpdatedAt = now.UTC()
		if _, err := s.db.ExecContext(
			ctx,
			`UPDATE users SET temporary_quota = ?, temporary_quota_date = ?, updated_at = ? WHERE id = ?`,
			user.TemporaryQuota,
			user.TemporaryQuotaDate,
			formatTime(user.UpdatedAt),
			user.ID,
		); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

func (s *Store) UpsertUserAPIKey(ctx context.Context, item domain.APIKey) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO api_keys (id, user_id, name, role, key_hash, enabled, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
			name = excluded.name,
			role = excluded.role,
			key_hash = excluded.key_hash,
			enabled = excluded.enabled,
			created_at = excluded.created_at,
			last_used_at = NULL`,
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
	Role    *domain.Role
}

func (s *Store) UpdateAPIKey(ctx context.Context, id string, userID string, update APIKeyUpdate) (domain.APIKey, error) {
	current, err := s.getAPIKey(ctx, id, userID)
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
	if update.Role != nil {
		current.Role = *update.Role
	}
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE api_keys SET name = ?, enabled = ?, key_hash = ?, role = ? WHERE id = ?`,
		current.Name,
		boolInt(current.Enabled),
		current.KeyHash,
		string(current.Role),
		id,
	)
	if err != nil {
		return domain.APIKey{}, err
	}
	return current, nil
}

func (s *Store) getAPIKey(ctx context.Context, id string, userID string) (domain.APIKey, error) {
	query := `SELECT id, user_id, name, role, key_hash, enabled, created_at, last_used_at FROM api_keys WHERE id = ?`
	args := []any{id}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	return scanAPIKey(s.db.QueryRowContext(ctx, query, args...))
}

func (s *Store) GetAPIKeyByUserID(ctx context.Context, userID string) (domain.APIKey, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT id, user_id, name, role, key_hash, enabled, created_at, last_used_at
		 FROM api_keys WHERE user_id = ?`,
		userID,
	)
	return scanAPIKey(row)
}

func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, formatTime(at), id)
	return err
}

func (s *Store) ListAccounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT access_token, password, type, status, quota, max_concurrency, image_quota_unknown, email, user_id, limits_progress_json, default_model_slug,
		 restore_at, recovery_state, recovery_error, success, fail, last_used_at, raw_json, created_at, updated_at
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

func (s *Store) UpsertAccountToken(ctx context.Context, token string, password string) (bool, error) {
	token = strings.TrimSpace(token)
	password = strings.TrimSpace(password)
	if token == "" {
		return false, nil
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(
		ctx,
		`INSERT INTO accounts (access_token, password, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(access_token) DO UPDATE SET
		   password = excluded.password,
		   updated_at = excluded.updated_at
		 WHERE excluded.password != ''`,
		token,
		password,
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
	Type           *string
	Status         *string
	Quota          *int
	Password       *string
	MaxConcurrency *int
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
	if update.Password != nil {
		current.Password = strings.TrimSpace(*update.Password)
	}
	if update.MaxConcurrency != nil {
		current.MaxConcurrency = *update.MaxConcurrency
		if current.MaxConcurrency < 0 {
			current.MaxConcurrency = 0
		}
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE accounts SET type = ?, status = ?, quota = ?, password = ?, max_concurrency = ?, updated_at = ? WHERE access_token = ?`,
		current.Type,
		current.Status,
		current.Quota,
		current.Password,
		current.MaxConcurrency,
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
		`SELECT access_token, password, type, status, quota, max_concurrency, image_quota_unknown, email, user_id, limits_progress_json, default_model_slug,
		 restore_at, recovery_state, recovery_error, success, fail, last_used_at, raw_json, created_at, updated_at
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
		     limits_progress_json = ?, default_model_slug = ?, restore_at = ?, recovery_state = ?, recovery_error = ?, raw_json = ?, updated_at = ?
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
		current.RecoveryState,
		current.RecoveryError,
		string(defaultJSON(current.RawJSON, `{}`)),
		formatTime(current.UpdatedAt),
		accessToken,
	)
	if err != nil {
		return domain.Account{}, err
	}
	return current, nil
}

func (s *Store) ReplaceAccountToken(ctx context.Context, oldToken string, newToken string) (domain.Account, error) {
	oldToken = strings.TrimSpace(oldToken)
	newToken = strings.TrimSpace(newToken)
	if oldToken == "" || newToken == "" {
		return domain.Account{}, fmt.Errorf("old and new access token are required")
	}
	if oldToken == newToken {
		return s.GetAccount(ctx, oldToken)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Account{}, err
	}
	defer tx.Rollback()
	current, err := scanAccount(tx.QueryRowContext(
		ctx,
		`SELECT access_token, password, type, status, quota, max_concurrency, image_quota_unknown, email, user_id, limits_progress_json, default_model_slug,
		 restore_at, recovery_state, recovery_error, success, fail, last_used_at, raw_json, created_at, updated_at
		 FROM accounts WHERE access_token = ?`,
		oldToken,
	))
	if err != nil {
		return domain.Account{}, err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO accounts (
			access_token, password, type, status, quota, max_concurrency, image_quota_unknown, email, user_id, limits_progress_json,
			default_model_slug, restore_at, recovery_state, recovery_error, success, fail, last_used_at, raw_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(access_token) DO UPDATE SET
			password = excluded.password,
			type = excluded.type,
			status = excluded.status,
			quota = excluded.quota,
			max_concurrency = excluded.max_concurrency,
			image_quota_unknown = excluded.image_quota_unknown,
			email = excluded.email,
			user_id = excluded.user_id,
			limits_progress_json = excluded.limits_progress_json,
			default_model_slug = excluded.default_model_slug,
			restore_at = excluded.restore_at,
			recovery_state = excluded.recovery_state,
			recovery_error = excluded.recovery_error,
			success = excluded.success,
			fail = excluded.fail,
			last_used_at = excluded.last_used_at,
			raw_json = excluded.raw_json,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		newToken,
		current.Password,
		current.Type,
		current.Status,
		current.Quota,
		current.MaxConcurrency,
		boolInt(current.ImageQuotaUnknown),
		current.Email,
		current.UserID,
		string(defaultJSON(current.LimitsProgress, `[]`)),
		current.DefaultModelSlug,
		current.RestoreAt,
		current.RecoveryState,
		current.RecoveryError,
		current.Success,
		current.Fail,
		current.LastUsedAt,
		string(defaultJSON(current.RawJSON, `{}`)),
		formatTime(current.CreatedAt),
		formatTime(time.Now().UTC()),
	)
	if err != nil {
		return domain.Account{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE access_token = ?`, oldToken); err != nil {
		return domain.Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Account{}, err
	}
	return s.GetAccount(ctx, newToken)
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

func (s *Store) SetAccountRecovery(ctx context.Context, accessToken string, state string, recoveryErr string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE accounts SET recovery_state = ?, recovery_error = ?, updated_at = ? WHERE access_token = ?`,
		strings.TrimSpace(state),
		strings.TrimSpace(recoveryErr),
		formatTime(time.Now().UTC()),
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
	fields := logIndexFieldsFromRaw(item.Detail)
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO system_logs (id, time, type, summary, detail_json, actor_id, subject_id, task_id, endpoint, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID,
		formatTime(item.Time),
		item.Type,
		item.Summary,
		string(item.Detail),
		firstNonEmpty(item.ActorID, fields.ActorID),
		firstNonEmpty(item.SubjectID, fields.SubjectID),
		firstNonEmpty(item.TaskID, fields.TaskID),
		firstNonEmpty(item.Endpoint, fields.Endpoint),
		firstNonEmpty(item.Status, fields.Status),
	)
	return err
}

func (s *Store) ListLogs(ctx context.Context, logType string, ids []string, includeDetail bool) ([]domain.SystemLog, error) {
	detailExpr := `NULL`
	if includeDetail {
		detailExpr = `detail_json`
	}
	query := `SELECT id, time, type, summary, actor_id, subject_id, task_id, endpoint, status, ` + detailExpr + ` FROM system_logs`
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
		if err := rows.Scan(&item.ID, &at, &item.Type, &item.Summary, &item.ActorID, &item.SubjectID, &item.TaskID, &item.Endpoint, &item.Status, &detail); err != nil {
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

type logIndexFields struct {
	ActorID   string
	SubjectID string
	TaskID    string
	Endpoint  string
	Status    string
}

func logIndexFieldsFromRaw(raw json.RawMessage) logIndexFields {
	if len(raw) == 0 {
		return logIndexFields{}
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		return logIndexFields{}
	}
	return logIndexFields{
		ActorID:   firstLogString(detail, "actor_id"),
		SubjectID: firstLogString(detail, "subject_id", "owner_id", "user_id"),
		TaskID:    firstLogString(detail, "task_id"),
		Endpoint:  firstLogString(detail, "endpoint"),
		Status:    firstLogString(detail, "status"),
	}
}

func firstLogString(detail map[string]any, keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(detail[key]))
		if value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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

func (s *Store) AddTaskEvent(ctx context.Context, item domain.TaskEvent) error {
	if len(item.Detail) == 0 {
		item.Detail = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO task_events (owner_id, task_id, id, time, type, summary, detail_json) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.OwnerID,
		item.TaskID,
		item.ID,
		formatTime(item.Time),
		item.Type,
		item.Summary,
		string(item.Detail),
	)
	return err
}

func (s *Store) ListTaskEvents(ctx context.Context, ownerID string, taskID string, includeDetail bool) ([]domain.TaskEvent, error) {
	detailExpr := `NULL`
	if includeDetail {
		detailExpr = `detail_json`
	}
	where := `task_id = ?`
	args := []any{strings.TrimSpace(taskID)}
	if ownerID = strings.TrimSpace(ownerID); ownerID != "" {
		where = `owner_id = ? AND ` + where
		args = append([]any{ownerID}, args...)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT owner_id, task_id, id, time, type, summary, `+detailExpr+`
		FROM task_events WHERE `+where+` ORDER BY time ASC, id ASC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.TaskEvent, 0)
	for rows.Next() {
		var item domain.TaskEvent
		var at string
		var detail sql.NullString
		if err := rows.Scan(&item.OwnerID, &item.TaskID, &item.ID, &at, &item.Type, &item.Summary, &detail); err != nil {
			return nil, err
		}
		item.Time = parseTime(at)
		if detail.Valid && detail.String != "" {
			item.Detail = json.RawMessage(detail.String)
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.TaskEvent{}
	}
	return items, rows.Err()
}

func (s *Store) CreateImageTask(ctx context.Context, task domain.ImageTask) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO image_tasks (owner_id, id, status, phase, mode, model, size, prompt, requested_count, reserved_quota_json, data_json, error, created_at, updated_at, deleted_at, deleted_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.OwnerID,
		task.ID,
		task.Status,
		task.Phase,
		task.Mode,
		task.Model,
		task.Size,
		task.Prompt,
		maxInt(1, task.RequestedCount),
		string(defaultJSON(task.ReservedQuota, `{}`)),
		nullJSON(task.Data),
		task.Error,
		formatTime(task.CreatedAt),
		formatTime(task.UpdatedAt),
		formatTimePtr(task.DeletedAt),
		task.DeletedBy,
	)
	return err
}

func (s *Store) ListImageTasks(ctx context.Context, ownerID string, ids []string, includeData bool, includeDeleted bool) ([]domain.ImageTask, error) {
	dataExpr := `NULL`
	if includeData {
		dataExpr = `data_json`
	}
	query := `SELECT owner_id, id, status, phase, mode, model, size, prompt, requested_count, reserved_quota_json, ` + dataExpr + `, error, created_at, updated_at, deleted_at, deleted_by FROM image_tasks WHERE 1=1`
	args := make([]any, 0, len(ids)+1)
	if ownerID = strings.TrimSpace(ownerID); ownerID != "" {
		query += ` AND owner_id = ?`
		args = append(args, ownerID)
	}
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
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

func (s *Store) DeleteImageTasks(ctx context.Context, ownerID string, deletedBy string, ids []string) (int, error) {
	refs := make([]ImageTaskRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, ImageTaskRef{OwnerID: ownerID, ID: id})
	}
	return s.DeleteImageTasksByRef(ctx, refs, deletedBy)
}

type ImageTaskRef struct {
	OwnerID string
	ID      string
}

func (s *Store) DeleteImageTasksByRef(ctx context.Context, refs []ImageTaskRef, deletedBy string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	removed := 0
	now := formatTime(time.Now().UTC())
	for _, ref := range refs {
		res, err := tx.ExecContext(ctx, `UPDATE image_tasks SET deleted_at = ?, deleted_by = ?, updated_at = ? WHERE owner_id = ? AND id = ? AND deleted_at IS NULL`, now, deletedBy, now, strings.TrimSpace(ref.OwnerID), strings.TrimSpace(ref.ID))
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		removed += int(affected)
	}
	return removed, tx.Commit()
}

func (s *Store) UpdateImageTask(ctx context.Context, ownerID string, id string, status string, phase string, data json.RawMessage, taskErr string) error {
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE image_tasks SET status = ?, phase = ?, data_json = ?, error = ?, updated_at = ? WHERE owner_id = ? AND id = ?`,
		status,
		phase,
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
		"refresh_account_concurrency":       4,
		"refresh_account_normal_batch_size": 8,
		"image_retention_days":              30,
		"image_poll_timeout_secs":           120,
		"image_account_concurrency":         1,
		"default_new_user_temporary_quota":  10,
		"public_registration_enabled":       false,
		"register_code_cooldown_seconds":    60,
		"register_allowed_email_domains":    []any{},
		"register_max_ordinary_users":       0,
		"auto_remove_invalid_accounts":      false,
		"auto_remove_rate_limited_accounts": false,
		"log_levels":                        []any{},
		"ai_review":                         map[string]any{"enabled": false, "base_url": "", "api_key": "", "model": "", "prompt": ""},
		"smtp_mail": map[string]any{
			"enabled":      false,
			"host":         "",
			"port":         587,
			"username":     "",
			"password":     "",
			"from_address": "",
			"from_name":    "",
			"reply_to":     "",
			"starttls":     true,
			"implicit_tls": false,
		},
		"backup": map[string]any{
			"enabled":              false,
			"schedule_hour":        24,
			"schedule_minute":      0,
			"keep_latest":          7,
			"encrypt":              true,
			"passphrase":           "",
			"r2_account_id":        "",
			"r2_access_key_id":     "",
			"r2_secret_access_key": "",
			"r2_bucket":            "",
			"r2_prefix":            "gpt-image-web",
			"include_env":          true,
			"include_compose":      true,
			"include_version":      true,
		},
		"register": map[string]any{
			"proxy":                  "",
			"mode":                   "total",
			"total":                  10,
			"threads":                3,
			"target_quota":           100,
			"target_available":       10,
			"check_interval_seconds": 5,
			"mail": map[string]any{
				"inbucket_api_base": "",
				"inbucket_domains":  []any{},
				"random_subdomain":  true,
			},
		},
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (domain.User, error) {
	var user domain.User
	var role string
	var status string
	var quotaUnlimited int
	var quotaUsedDate string
	var createdAt string
	var updatedAt string
	var lastLogin sql.NullString
	err := row.Scan(&user.ID, &user.Email, &user.Name, &user.PasswordHash, &role, &status, &quotaUnlimited, &user.PermanentQuota, &user.TemporaryQuota, &user.TemporaryQuotaDate, &user.DailyTemporaryQuota, &user.QuotaUsedTotal, &user.QuotaUsedToday, &quotaUsedDate, &createdAt, &updatedAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	user.Role = domain.Role(role)
	user.Status = domain.UserStatus(status)
	user.QuotaUnlimited = quotaUnlimited == 1
	user.QuotaUsedDate = strings.TrimSpace(quotaUsedDate)
	user.CreatedAt = parseTime(createdAt)
	user.UpdatedAt = parseTime(updatedAt)
	now := time.Now()
	applyDailyTemporaryQuota(&user, now)
	user.AvailableQuota = availableUserQuota(user, now)
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
		&item.Password,
		&item.Type,
		&item.Status,
		&item.Quota,
		&item.MaxConcurrency,
		&imageQuotaUnknown,
		&item.Email,
		&item.UserID,
		&limitsProgress,
		&item.DefaultModelSlug,
		&item.RestoreAt,
		&item.RecoveryState,
		&item.RecoveryError,
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
	var reservedQuota string
	var data sql.NullString
	var createdAt string
	var updatedAt string
	var deletedAt sql.NullString
	err := row.Scan(&item.OwnerID, &item.ID, &item.Status, &item.Phase, &item.Mode, &item.Model, &item.Size, &item.Prompt, &item.RequestedCount, &reservedQuota, &data, &item.Error, &createdAt, &updatedAt, &deletedAt, &item.DeletedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ImageTask{}, ErrNotFound
	}
	if err != nil {
		return domain.ImageTask{}, err
	}
	if strings.TrimSpace(reservedQuota) != "" {
		item.ReservedQuota = json.RawMessage(reservedQuota)
	}
	if data.Valid && data.String != "" {
		item.Data = json.RawMessage(data.String)
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	if deletedAt.Valid && strings.TrimSpace(deletedAt.String) != "" {
		parsed := parseTime(deletedAt.String)
		item.DeletedAt = &parsed
	}
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

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func nullJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}
