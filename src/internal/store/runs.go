package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type AccountScriptJob struct {
	ID        int64  `json:"id"`
	AccountID int64  `json:"account_id"`
	ScriptKey string `json:"script_key"`
	QLCronID  int64  `json:"ql_cron_id"`
	Schedule  string `json:"schedule"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// UserScriptJob 是登录账号级的定时任务：一个网关登录账号 + 一个脚本一条。
type UserScriptJob struct {
	ID              int64  `json:"id"`
	OwnerUserID     int64  `json:"owner_user_id"`
	ScriptKey       string `json:"script_key"`
	QLCronID        int64  `json:"ql_cron_id"`
	Schedule        string `json:"schedule"`
	AnchorAccountID int64  `json:"anchor_account_id"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

// AccountScriptJobRef 是「某个账号挂了某个脚本」的轻量引用，用于收敛旧数据。
type AccountScriptJobRef struct {
	AccountID int64  `json:"account_id"`
	ScriptKey string `json:"script_key"`
	QLCronID  int64  `json:"ql_cron_id"`
}

func (db *DB) ListUserScriptJobs(ctx context.Context, ownerUserID int64) ([]UserScriptJob, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, owner_user_id, script_key, ql_cron_id, schedule, anchor_account_id, created_at, updated_at
FROM user_script_jobs WHERE owner_user_id=? ORDER BY script_key`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UserScriptJob, 0)
	for rows.Next() {
		var job UserScriptJob
		if err := rows.Scan(&job.ID, &job.OwnerUserID, &job.ScriptKey, &job.QLCronID, &job.Schedule, &job.AnchorAccountID, &job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (db *DB) GetUserScriptJob(ctx context.Context, ownerUserID int64, scriptKey string) (*UserScriptJob, error) {
	var job UserScriptJob
	err := db.sql.QueryRowContext(ctx, `
SELECT id, owner_user_id, script_key, ql_cron_id, schedule, anchor_account_id, created_at, updated_at
FROM user_script_jobs WHERE owner_user_id=? AND script_key=?`, ownerUserID, scriptKey).
		Scan(&job.ID, &job.OwnerUserID, &job.ScriptKey, &job.QLCronID, &job.Schedule, &job.AnchorAccountID, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (db *DB) UpsertUserScriptJob(ctx context.Context, ownerUserID int64, scriptKey string, qlCronID int64, schedule string, anchorAccountID int64) (*UserScriptJob, error) {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO user_script_jobs(owner_user_id, script_key, ql_cron_id, schedule, anchor_account_id, created_at, updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(owner_user_id, script_key) DO UPDATE SET
ql_cron_id=excluded.ql_cron_id, schedule=excluded.schedule, anchor_account_id=excluded.anchor_account_id, updated_at=excluded.updated_at`,
		ownerUserID, scriptKey, qlCronID, schedule, anchorAccountID, now, now)
	if err != nil {
		return nil, err
	}
	return db.GetUserScriptJob(ctx, ownerUserID, scriptKey)
}

func (db *DB) DeleteUserScriptJob(ctx context.Context, ownerUserID int64, scriptKey string) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM user_script_jobs WHERE owner_user_id=? AND script_key=?", ownerUserID, scriptKey)
	return err
}

// ListOwnerScriptJobRefs 取指定 code 账号集合上的「账号级」任务轻量引用。
//
// 合并说明：上游 0.3.x 把账号归属迁到 auth.account_owners（auth 库，另一个 sqlite
// 文件），wechat_accounts.owner_user_id 已删除，因此这里不再自己 join 归属列，
// 而是由调用方先把「某个登录账号名下的 code 账号」算好再按 id 列表过滤。
func (db *DB) ListOwnerScriptJobRefs(ctx context.Context, accountIDs []int64) ([]AccountScriptJobRef, error) {
	if len(accountIDs) == 0 {
		return nil, nil
	}
	// 账号数量级很小（单机几十个），直接展开占位符；上限兜底避免超长 SQL。
	if len(accountIDs) > 1000 {
		accountIDs = accountIDs[:1000]
	}
	placeholders := make([]string, 0, len(accountIDs))
	args := make([]any, 0, len(accountIDs))
	for _, id := range accountIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := `
SELECT j.account_id, j.script_key, j.ql_cron_id
FROM account_script_jobs j
WHERE j.account_id IN (` + strings.Join(placeholders, ",") + `)
ORDER BY j.account_id, j.script_key`
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AccountScriptJobRef, 0)
	for rows.Next() {
		var ref AccountScriptJobRef
		if err := rows.Scan(&ref.AccountID, &ref.ScriptKey, &ref.QLCronID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

type AccountPushSetting struct {
	AccountID    int64  `json:"account_id"`
	Channel      string `json:"channel"`
	TokenEnvName string `json:"-"`
	TopicEnvName string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

func (db *DB) ListAccountScriptJobs(ctx context.Context, accountID int64) ([]AccountScriptJob, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, account_id, script_key, ql_cron_id, schedule, created_at, updated_at
FROM account_script_jobs WHERE account_id=? ORDER BY script_key`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AccountScriptJob, 0)
	for rows.Next() {
		var job AccountScriptJob
		if err := rows.Scan(&job.ID, &job.AccountID, &job.ScriptKey, &job.QLCronID, &job.Schedule, &job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (db *DB) GetAccountScriptJob(ctx context.Context, accountID int64, scriptKey string) (*AccountScriptJob, error) {
	var job AccountScriptJob
	err := db.sql.QueryRowContext(ctx, `
SELECT id, account_id, script_key, ql_cron_id, schedule, created_at, updated_at
FROM account_script_jobs WHERE account_id=? AND script_key=?`, accountID, scriptKey).
		Scan(&job.ID, &job.AccountID, &job.ScriptKey, &job.QLCronID, &job.Schedule, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (db *DB) UpsertAccountScriptJob(ctx context.Context, accountID int64, scriptKey string, qlCronID int64, schedule string) (*AccountScriptJob, error) {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO account_script_jobs(account_id, script_key, ql_cron_id, schedule, created_at, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(account_id, script_key) DO UPDATE SET
ql_cron_id=excluded.ql_cron_id, schedule=excluded.schedule, updated_at=excluded.updated_at`,
		accountID, scriptKey, qlCronID, schedule, now, now)
	if err != nil {
		return nil, err
	}
	return db.GetAccountScriptJob(ctx, accountID, scriptKey)
}

func (db *DB) DeleteAccountScriptJob(ctx context.Context, accountID int64, scriptKey string) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM account_script_jobs WHERE account_id=? AND script_key=?", accountID, scriptKey)
	return err
}

func (db *DB) GetAccountPushSetting(ctx context.Context, accountID int64) (*AccountPushSetting, error) {
	var setting AccountPushSetting
	err := db.sql.QueryRowContext(ctx, `
SELECT account_id, channel, token_env_name, topic_env_name, created_at, updated_at
FROM account_push_settings WHERE account_id=?`, accountID).
		Scan(&setting.AccountID, &setting.Channel, &setting.TokenEnvName, &setting.TopicEnvName, &setting.CreatedAt, &setting.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &setting, nil
}

func (db *DB) UpsertAccountPushSetting(ctx context.Context, accountID int64, channel, tokenEnvName, topicEnvName string) (*AccountPushSetting, error) {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO account_push_settings(account_id, channel, token_env_name, topic_env_name, created_at, updated_at)
VALUES(?,?,?,?,?,?)
ON CONFLICT(account_id) DO UPDATE SET
channel=excluded.channel, token_env_name=excluded.token_env_name,
topic_env_name=excluded.topic_env_name, updated_at=excluded.updated_at`,
		accountID, channel, tokenEnvName, topicEnvName, now, now)
	if err != nil {
		return nil, err
	}
	return db.GetAccountPushSetting(ctx, accountID)
}

func (db *DB) AccountPushSettingOrDefault(ctx context.Context, accountID int64) (*AccountPushSetting, error) {
	setting, err := db.GetAccountPushSetting(ctx, accountID)
	if err == nil {
		return setting, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	return &AccountPushSetting{AccountID: accountID, Channel: "none"}, nil
}
