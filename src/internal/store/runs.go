package store

import (
	"context"
	"database/sql"
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
