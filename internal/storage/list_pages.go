package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"gpt-image-web/internal/domain"
)

type AccountListQuery struct {
	Page       int
	PageSize   int
	Query      string
	Status     string
	Type       string
	ActiveOnly bool
}

type AccountListSummary struct {
	Total            int  `json:"total"`
	Normal           int  `json:"normal"`
	Success          int  `json:"success"`
	Fail             int  `json:"fail"`
	QuotaTotal       int  `json:"quota_total"`
	QuotaUnknown     bool `json:"quota_unknown"`
	QuotaUnlimited   bool `json:"quota_unlimited"`
	ActiveRequests   int  `json:"active_requests"`
	TotalConcurrency int  `json:"total_concurrency"`
}

type UserListQuery struct {
	Page     int
	PageSize int
	Query    string
	Status   string
	Role     string
}

type LogListQuery struct {
	Page          int
	PageSize      int
	Query         string
	Type          string
	ActorID       string
	SubjectID     string
	TaskID        string
	Endpoint      string
	Status        string
	DateFrom      string
	DateTo        string
	IncludeDetail bool
}

type ImageTaskPageQuery struct {
	Page           int
	PageSize       int
	OwnerID        string
	Query          string
	Status         string
	Mode           string
	Model          string
	Size           string
	DateFrom       string
	DateTo         string
	Deleted        string
	IncludeDeleted bool
}

func (s *Store) ListAccountsPage(ctx context.Context, query AccountListQuery) ([]domain.Account, int, AccountListSummary, error) {
	page, pageSize := normalizePage(query.Page, query.PageSize)
	where, args := buildAccountWhere(query)

	total, err := countWithWhere(ctx, s.db, `SELECT COUNT(*) FROM accounts`, where, args)
	if err != nil {
		return nil, 0, AccountListSummary{}, err
	}

	summaryQuery := `SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN status = '正常' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(success), 0),
		COALESCE(SUM(fail), 0),
		COALESCE(SUM(CASE WHEN status = '正常' AND image_quota_unknown = 0 THEN quota ELSE 0 END), 0),
		COALESCE(MAX(CASE WHEN status = '正常' AND image_quota_unknown = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(MAX(CASE WHEN status = '正常' AND LOWER(type) IN ('pro', 'prolite') THEN 1 ELSE 0 END), 0)
		FROM accounts`
	row := s.db.QueryRowContext(ctx, summaryQuery+where, args...)
	var summary AccountListSummary
	var quotaUnknown int
	var quotaUnlimited int
	if err := row.Scan(&summary.Total, &summary.Normal, &summary.Success, &summary.Fail, &summary.QuotaTotal, &quotaUnknown, &quotaUnlimited); err != nil {
		return nil, 0, AccountListSummary{}, err
	}
	summary.QuotaUnknown = quotaUnknown > 0
	summary.QuotaUnlimited = quotaUnlimited > 0

	itemsQuery := `SELECT access_token, password, type, status, quota, max_concurrency, image_quota_unknown, email, user_id, limits_progress_json, default_model_slug,
		restore_at, recovery_state, recovery_error, success, fail, last_used_at, raw_json, created_at, updated_at
		FROM accounts` + where + ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	itemsArgs := append(cloneArgs(args), pageSize, pageOffset(page, pageSize))
	rows, err := s.db.QueryContext(ctx, itemsQuery, itemsArgs...)
	if err != nil {
		return nil, 0, AccountListSummary{}, err
	}
	defer rows.Close()
	items := make([]domain.Account, 0, pageSize)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, 0, AccountListSummary{}, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.Account{}
	}
	return items, total, summary, rows.Err()
}

func (s *Store) ListUsersWithAPIKeysPage(ctx context.Context, query UserListQuery) ([]domain.User, int, error) {
	page, pageSize := normalizePage(query.Page, query.PageSize)
	where, args := buildUserWhere(query)

	total, err := countWithWhere(ctx, s.db, `SELECT COUNT(*) FROM users`, where, args)
	if err != nil {
		return nil, 0, err
	}

	itemsQuery := `SELECT id, email, name, password_hash, role, status, quota_unlimited, permanent_quota, temporary_quota, temporary_quota_date, daily_temporary_quota, quota_used_total, quota_used_today, quota_used_date, created_at, updated_at, last_login_at
		FROM users` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	itemsArgs := append(cloneArgs(args), pageSize, pageOffset(page, pageSize))
	rows, err := s.db.QueryContext(ctx, itemsQuery, itemsArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]domain.User, 0, pageSize)
	now := time.Now()
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		user, err = s.refreshUserDailyTemporaryQuota(ctx, user, now)
		if err != nil {
			return nil, 0, err
		}
		if key, err := s.GetAPIKeyByUserID(ctx, user.ID); err == nil {
			user.APIKey = &key
		} else if !errors.Is(err, ErrNotFound) {
			return nil, 0, err
		}
		items = append(items, user)
	}
	if items == nil {
		items = []domain.User{}
	}
	return items, total, rows.Err()
}

func (s *Store) ListLogsPage(ctx context.Context, query LogListQuery) ([]domain.SystemLog, int, error) {
	page, pageSize := normalizePage(query.Page, query.PageSize)
	where, args := buildLogWhere(query)

	total, err := countWithWhere(ctx, s.db, `SELECT COUNT(*) FROM system_logs`, where, args)
	if err != nil {
		return nil, 0, err
	}

	detailExpr := `NULL`
	if query.IncludeDetail {
		detailExpr = `detail_json`
	}
	itemsQuery := `SELECT id, time, type, summary, actor_id, subject_id, task_id, endpoint, status, ` + detailExpr + ` FROM system_logs` + where + ` ORDER BY time DESC LIMIT ? OFFSET ?`
	itemsArgs := append(cloneArgs(args), pageSize, pageOffset(page, pageSize))
	rows, err := s.db.QueryContext(ctx, itemsQuery, itemsArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]domain.SystemLog, 0, pageSize)
	for rows.Next() {
		var item domain.SystemLog
		var at string
		var detail sql.NullString
		if err := rows.Scan(&item.ID, &at, &item.Type, &item.Summary, &item.ActorID, &item.SubjectID, &item.TaskID, &item.Endpoint, &item.Status, &detail); err != nil {
			return nil, 0, err
		}
		item.Time = parseTime(at)
		if detail.Valid && detail.String != "" {
			item.Detail = []byte(detail.String)
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.SystemLog{}
	}
	return items, total, rows.Err()
}

func (s *Store) ListImageTasksPage(ctx context.Context, ownerID string, query ImageTaskPageQuery) ([]domain.ImageTask, int, error) {
	page, pageSize := normalizePage(query.Page, query.PageSize)
	where, args := buildTaskWhere(ownerID, query)

	total, err := countWithWhere(ctx, s.db, `SELECT COUNT(*) FROM image_tasks`, where, args)
	if err != nil {
		return nil, 0, err
	}

	itemsQuery := `SELECT owner_id, id, status, phase, mode, model, size, prompt, requested_count, reserved_quota_json, NULL, error, created_at, updated_at, deleted_at, deleted_by
		FROM image_tasks` + where + ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	itemsArgs := append(cloneArgs(args), pageSize, pageOffset(page, pageSize))
	rows, err := s.db.QueryContext(ctx, itemsQuery, itemsArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]domain.ImageTask, 0, pageSize)
	for rows.Next() {
		item, err := scanImageTask(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []domain.ImageTask{}
	}
	return items, total, rows.Err()
}

func buildAccountWhere(query AccountListQuery) (string, []any) {
	clauses := []string{`1=1`}
	args := make([]any, 0, 8)
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, `status = ?`)
		args = append(args, status)
	}
	if accountType := strings.TrimSpace(query.Type); accountType != "" {
		clauses = append(clauses, `type = ?`)
		args = append(args, accountType)
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(email) LIKE ? OR LOWER(password) LIKE ? OR LOWER(type) LIKE ? OR LOWER(status) LIKE ? OR LOWER(default_model_slug) LIKE ?)`)
		args = append(args, like, like, like, like, like)
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func buildUserWhere(query UserListQuery) (string, []any) {
	clauses := []string{`status != 'deleted'`}
	args := make([]any, 0, 6)
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, `status = ?`)
		args = append(args, status)
	}
	if role := strings.TrimSpace(query.Role); role != "" {
		clauses = append(clauses, `role = ?`)
		args = append(args, role)
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(email) LIKE ? OR LOWER(name) LIKE ?)`)
		args = append(args, like, like)
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func buildLogWhere(query LogListQuery) (string, []any) {
	clauses := []string{`1=1`}
	args := make([]any, 0, 12)
	if logType := strings.TrimSpace(query.Type); logType != "" {
		clauses = append(clauses, `type = ?`)
		args = append(args, logType)
	}
	if actorID := strings.TrimSpace(query.ActorID); actorID != "" {
		clauses = append(clauses, `actor_id = ?`)
		args = append(args, actorID)
	}
	if subjectID := strings.TrimSpace(query.SubjectID); subjectID != "" {
		clauses = append(clauses, `subject_id = ?`)
		args = append(args, subjectID)
	}
	if taskID := strings.TrimSpace(query.TaskID); taskID != "" {
		clauses = append(clauses, `task_id = ?`)
		args = append(args, taskID)
	}
	if endpoint := strings.TrimSpace(query.Endpoint); endpoint != "" {
		clauses = append(clauses, `endpoint = ?`)
		args = append(args, endpoint)
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, `status = ?`)
		args = append(args, status)
	}
	if from := strings.TrimSpace(query.DateFrom); from != "" {
		clauses = append(clauses, `time >= ?`)
		args = append(args, normalizeDateBoundary(from, false))
	}
	if to := strings.TrimSpace(query.DateTo); to != "" {
		clauses = append(clauses, `time <= ?`)
		args = append(args, normalizeDateBoundary(to, true))
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(summary) LIKE ? OR LOWER(detail_json) LIKE ? OR LOWER(actor_id) LIKE ? OR LOWER(subject_id) LIKE ? OR LOWER(task_id) LIKE ? OR LOWER(endpoint) LIKE ? OR LOWER(status) LIKE ?)`)
		args = append(args, like, like, like, like, like, like, like)
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func buildTaskWhere(ownerID string, query ImageTaskPageQuery) (string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	if ownerID = strings.TrimSpace(ownerID); ownerID != "" {
		clauses = append(clauses, `owner_id = ?`)
		args = append(args, ownerID)
	}
	if queryOwnerID := strings.TrimSpace(query.OwnerID); queryOwnerID != "" {
		clauses = append(clauses, `owner_id = ?`)
		args = append(args, queryOwnerID)
	}
	switch strings.TrimSpace(query.Deleted) {
	case "only":
		clauses = append(clauses, `deleted_at IS NOT NULL`)
	case "active":
		clauses = append(clauses, `deleted_at IS NULL`)
	default:
		if !query.IncludeDeleted {
			clauses = append(clauses, `deleted_at IS NULL`)
		}
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, `status = ?`)
		args = append(args, status)
	}
	if mode := strings.TrimSpace(query.Mode); mode != "" {
		clauses = append(clauses, `mode = ?`)
		args = append(args, mode)
	}
	if model := strings.TrimSpace(query.Model); model != "" {
		clauses = append(clauses, `model = ?`)
		args = append(args, model)
	}
	if size := strings.TrimSpace(query.Size); size != "" {
		clauses = append(clauses, `size = ?`)
		args = append(args, size)
	}
	if from := strings.TrimSpace(query.DateFrom); from != "" {
		clauses = append(clauses, `created_at >= ?`)
		args = append(args, normalizeDateBoundary(from, false))
	}
	if to := strings.TrimSpace(query.DateTo); to != "" {
		clauses = append(clauses, `created_at <= ?`)
		args = append(args, normalizeDateBoundary(to, true))
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(id) LIKE ? OR LOWER(mode) LIKE ? OR LOWER(status) LIKE ? OR LOWER(model) LIKE ? OR LOWER(size) LIKE ? OR LOWER(prompt) LIKE ? OR LOWER(error) LIKE ? OR LOWER(deleted_by) LIKE ?)`)
		args = append(args, like, like, like, like, like, like, like, like)
	}
	if len(clauses) == 0 {
		return ` WHERE 1=1`, args
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func normalizeDateBoundary(value string, end bool) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if len(value) == len("2006-01-02") && strings.Count(value, "-") == 2 {
		if end {
			return value + "T23:59:59.999999999Z"
		}
		return value + "T00:00:00Z"
	}
	return value
}

func countWithWhere(ctx context.Context, db *sql.DB, base string, where string, args []any) (int, error) {
	var total int
	err := db.QueryRowContext(ctx, base+where, args...).Scan(&total)
	return total, err
}

func normalizePage(page int, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

func pageOffset(page int, pageSize int) int {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	return (page - 1) * pageSize
}

func cloneArgs(args []any) []any {
	if len(args) == 0 {
		return nil
	}
	out := make([]any, len(args))
	copy(out, args)
	return out
}
