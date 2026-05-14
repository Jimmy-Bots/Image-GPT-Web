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
	DueOnly    bool
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
		COALESCE(SUM(CASE WHEN accounts.status = '正常' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(accounts.success), 0),
		COALESCE(SUM(accounts.fail), 0),
		COALESCE(SUM(CASE WHEN accounts.status = '正常' AND accounts.image_quota_unknown = 0 THEN accounts.quota ELSE 0 END), 0),
		COALESCE(MAX(CASE WHEN accounts.status = '正常' AND accounts.image_quota_unknown = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(MAX(CASE WHEN accounts.status = '正常' AND LOWER(accounts.type) IN ('pro', 'prolite') THEN 1 ELSE 0 END), 0)
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
		FROM accounts` + where + ` ORDER BY accounts.updated_at DESC LIMIT ? OFFSET ?`
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
		FROM users` + where + ` ORDER BY users.created_at DESC LIMIT ? OFFSET ?`
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
	itemsQuery = strings.Replace(itemsQuery, " ORDER BY time DESC", " ORDER BY system_logs.time DESC", 1)
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
	countBase := `SELECT COUNT(*) FROM image_tasks`
	if strings.TrimSpace(query.Query) != "" {
		countBase = `SELECT COUNT(*) FROM image_tasks LEFT JOIN users ON users.id = image_tasks.owner_id`
	}
	total, err := countWithWhere(ctx, s.db, countBase, where, args)
	if err != nil {
		return nil, 0, err
	}

	itemsQuery := `SELECT image_tasks.owner_id, users.email, users.name, users.role, image_tasks.id, image_tasks.status, image_tasks.phase, image_tasks.mode, image_tasks.model, image_tasks.size, image_tasks.prompt, image_tasks.requested_count, image_tasks.reserved_quota_json, NULL, image_tasks.reference_data_json, image_tasks.error, image_tasks.created_at, image_tasks.updated_at, image_tasks.deleted_at, image_tasks.deleted_by
		FROM image_tasks
		LEFT JOIN users ON users.id = image_tasks.owner_id` + where + ` ORDER BY image_tasks.updated_at DESC LIMIT ? OFFSET ?`
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
		clauses = append(clauses, `accounts.status = ?`)
		args = append(args, status)
	}
	if accountType := strings.TrimSpace(query.Type); accountType != "" {
		clauses = append(clauses, `accounts.type = ?`)
		args = append(args, accountType)
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(accounts.email) LIKE ? OR LOWER(accounts.password) LIKE ? OR LOWER(accounts.type) LIKE ? OR LOWER(accounts.status) LIKE ? OR LOWER(accounts.default_model_slug) LIKE ?)`)
		args = append(args, like, like, like, like, like)
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func buildUserWhere(query UserListQuery) (string, []any) {
	clauses := []string{`status != 'deleted'`}
	args := make([]any, 0, 6)
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, `users.status = ?`)
		args = append(args, status)
	}
	if role := strings.TrimSpace(query.Role); role != "" {
		clauses = append(clauses, `users.role = ?`)
		args = append(args, role)
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(users.email) LIKE ? OR LOWER(users.name) LIKE ?)`)
		args = append(args, like, like)
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func buildLogWhere(query LogListQuery) (string, []any) {
	clauses := []string{`1=1`}
	args := make([]any, 0, 12)
	if logType := strings.TrimSpace(query.Type); logType != "" {
		clauses = append(clauses, `system_logs.type = ?`)
		args = append(args, logType)
	}
	if actorID := strings.TrimSpace(query.ActorID); actorID != "" {
		clauses = append(clauses, `system_logs.actor_id = ?`)
		args = append(args, actorID)
	}
	if subjectID := strings.TrimSpace(query.SubjectID); subjectID != "" {
		clauses = append(clauses, `system_logs.subject_id = ?`)
		args = append(args, subjectID)
	}
	if taskID := strings.TrimSpace(query.TaskID); taskID != "" {
		clauses = append(clauses, `system_logs.task_id = ?`)
		args = append(args, taskID)
	}
	if endpoint := strings.TrimSpace(query.Endpoint); endpoint != "" {
		clauses = append(clauses, `system_logs.endpoint = ?`)
		args = append(args, endpoint)
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, `system_logs.status = ?`)
		args = append(args, status)
	}
	if from := strings.TrimSpace(query.DateFrom); from != "" {
		clauses = append(clauses, `system_logs.time >= ?`)
		args = append(args, normalizeDateBoundary(from, false))
	}
	if to := strings.TrimSpace(query.DateTo); to != "" {
		clauses = append(clauses, `system_logs.time <= ?`)
		args = append(args, normalizeDateBoundary(to, true))
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(system_logs.summary) LIKE ? OR LOWER(system_logs.detail_json) LIKE ? OR LOWER(system_logs.actor_id) LIKE ? OR LOWER(system_logs.subject_id) LIKE ? OR LOWER(system_logs.task_id) LIKE ? OR LOWER(system_logs.endpoint) LIKE ? OR LOWER(system_logs.status) LIKE ?)`)
		args = append(args, like, like, like, like, like, like, like)
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

func buildTaskWhere(ownerID string, query ImageTaskPageQuery) (string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	if ownerID = strings.TrimSpace(ownerID); ownerID != "" {
		clauses = append(clauses, `image_tasks.owner_id = ?`)
		args = append(args, ownerID)
	}
	if queryOwnerID := strings.TrimSpace(query.OwnerID); queryOwnerID != "" {
		clauses = append(clauses, `image_tasks.owner_id = ?`)
		args = append(args, queryOwnerID)
	}
	switch strings.TrimSpace(query.Deleted) {
	case "only":
		clauses = append(clauses, `image_tasks.deleted_at IS NOT NULL`)
	case "active":
		clauses = append(clauses, `image_tasks.deleted_at IS NULL`)
	default:
		if !query.IncludeDeleted {
			clauses = append(clauses, `image_tasks.deleted_at IS NULL`)
		}
	}
	if status := strings.TrimSpace(query.Status); status != "" {
		clauses = append(clauses, `image_tasks.status = ?`)
		args = append(args, status)
	}
	if mode := strings.TrimSpace(query.Mode); mode != "" {
		clauses = append(clauses, `image_tasks.mode = ?`)
		args = append(args, mode)
	}
	if model := strings.TrimSpace(query.Model); model != "" {
		clauses = append(clauses, `image_tasks.model = ?`)
		args = append(args, model)
	}
	if size := strings.TrimSpace(query.Size); size != "" {
		clauses = append(clauses, `image_tasks.size = ?`)
		args = append(args, size)
	}
	if from := strings.TrimSpace(query.DateFrom); from != "" {
		clauses = append(clauses, `image_tasks.created_at >= ?`)
		args = append(args, normalizeDateBoundary(from, false))
	}
	if to := strings.TrimSpace(query.DateTo); to != "" {
		clauses = append(clauses, `image_tasks.created_at <= ?`)
		args = append(args, normalizeDateBoundary(to, true))
	}
	if text := strings.ToLower(strings.TrimSpace(query.Query)); text != "" {
		like := "%" + text + "%"
		clauses = append(clauses, `(LOWER(image_tasks.id) LIKE ? OR LOWER(image_tasks.mode) LIKE ? OR LOWER(image_tasks.status) LIKE ? OR LOWER(image_tasks.model) LIKE ? OR LOWER(image_tasks.size) LIKE ? OR LOWER(image_tasks.prompt) LIKE ? OR LOWER(image_tasks.error) LIKE ? OR LOWER(image_tasks.deleted_by) LIKE ? OR LOWER(COALESCE(users.email, '')) LIKE ? OR LOWER(COALESCE(users.name, '')) LIKE ?)`)
		args = append(args, like, like, like, like, like, like, like, like, like, like)
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
