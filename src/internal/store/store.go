package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS wechat_accounts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    openid          TEXT    NOT NULL UNIQUE,
    uin             INTEGER,
    alias           TEXT,
    nickname        TEXT,
    remark          TEXT,
    avatar          TEXT,
    user_info       TEXT,
    login_buffer    TEXT    NOT NULL,
    credentials     TEXT,
    status          TEXT,
    owner_user_id   INTEGER,
    api_shared      INTEGER NOT NULL DEFAULT 1,
    last_checked_at INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wxacc_uin ON wechat_accounts(uin);
-- idx_wxacc_owner 由 migrateAccountOwnership 在补列之后再建
-- （老库先跑 schema 时 owner_user_id 还不存在，直接建索引会报 no such column）

CREATE TABLE IF NOT EXISTS sessions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    wechat_account_id INTEGER NOT NULL REFERENCES wechat_accounts(id) ON DELETE CASCADE,
    uin               INTEGER,
    tcp_proxy         TEXT    NOT NULL DEFAULT '',
    session_blob      TEXT    NOT NULL,
    expires_at        INTEGER NOT NULL,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    UNIQUE(wechat_account_id, tcp_proxy)
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS features (
    code        INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT,
    enabled     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS account_script_jobs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id    INTEGER NOT NULL REFERENCES wechat_accounts(id) ON DELETE CASCADE,
    script_key    TEXT    NOT NULL,
    ql_cron_id    INTEGER NOT NULL,
    schedule      TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE(account_id, script_key)
);
CREATE INDEX IF NOT EXISTS idx_account_script_jobs_account ON account_script_jobs(account_id);

CREATE TABLE IF NOT EXISTS account_push_settings (
    account_id     INTEGER PRIMARY KEY REFERENCES wechat_accounts(id) ON DELETE CASCADE,
    channel        TEXT    NOT NULL DEFAULT 'none',
    token_env_name TEXT    NOT NULL DEFAULT '',
    topic_env_name TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
`

var defaultFeatures = []Feature{
	{Code: 1001, Name: "getCode", Description: stringPtr("wx.login code"), Enabled: true},
	{Code: 1002, Name: "getPhoneNumber", Description: stringPtr("取手机号"), Enabled: true},
	{Code: 1003, Name: "operateWxData", Description: stringPtr("通用云函数代理"), Enabled: true},
}

type DB struct {
	sql *sql.DB
}

type WechatAccount struct {
	ID            int64          `json:"id"`
	OpenID        string         `json:"openid"`
	UIN           *int64         `json:"uin,omitempty"`
	Alias         *string        `json:"alias,omitempty"`
	Nickname      *string        `json:"nickname,omitempty"`
	Remark        *string        `json:"remark,omitempty"`
	Avatar        *string        `json:"avatar,omitempty"`
	UserInfo      map[string]any `json:"user_info,omitempty"`
	LoginBuffer   string         `json:"login_buffer,omitempty"`
	Credentials   map[string]any `json:"credentials,omitempty"`
	Status        *string        `json:"status,omitempty"`
	// OwnerUserID 为账号归属的控制台用户；NULL 表示未归属（老数据/未启用鉴权时创建），
	// 只有管理员可见可管。
	OwnerUserID   *int64         `json:"owner_user_id,omitempty"`
	// APIShared 为是否允许无会话的脚本/API（/instances、/wxapp/* 等）读取本账号，默认允许。
	APIShared     bool           `json:"api_shared"`
	LastCheckedAt *int64         `json:"last_checked_at,omitempty"`
	CreatedAt     int64          `json:"created_at"`
	UpdatedAt     int64          `json:"updated_at"`
}

type AccountPublic struct {
	ID            int64   `json:"id"`
	OpenID        string  `json:"openid"`
	UIN           *int64  `json:"uin"`
	Alias         *string `json:"alias"`
	Nickname      *string `json:"nickname"`
	Remark        *string `json:"remark"`
	Avatar        *string `json:"avatar"`
	Status        *string `json:"status"`
	OwnerUserID   *int64  `json:"owner_user_id"`
	APIShared      bool    `json:"api_shared"`
	LastCheckedAt *int64  `json:"last_checked_at"`
	CreatedAt     int64   `json:"created_at"`
	UpdatedAt     int64   `json:"updated_at"`
}

type SessionRow struct {
	ID              int64
	WechatAccountID int64
	UIN             *int64
	TCPProxy        string
	SessionBlob     map[string]any
	ExpiresAt       int64
	CreatedAt       int64
	UpdatedAt       int64
}

type Feature struct {
	Code        int     `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Enabled     bool    `json:"enabled"`
}

func Open(path string) (*DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		_, _ = db.ExecContext(ctx, "PRAGMA journal_mode=WAL")
	}
	_, _ = db.ExecContext(ctx, "PRAGMA synchronous=NORMAL")
	_, _ = db.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	if err = migrateSessionsTable(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = migrateAccountRemark(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = migrateAccountOwnership(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	out := &DB{sql: db}
	if err = out.EnsureDefaultFeatures(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return out, nil
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

func (db *DB) EnsureDefaultFeatures(ctx context.Context) error {
	for _, f := range defaultFeatures {
		desc := nullableString(f.Description)
		if _, err := db.sql.ExecContext(ctx,
			"INSERT OR IGNORE INTO features(code, name, description, enabled) VALUES(?,?,?,1)",
			f.Code, f.Name, desc,
		); err != nil {
			return err
		}
	}
	return nil
}

func migrateSessionsTable(ctx context.Context, db *sql.DB) error {
	oldExists, err := sqliteTableExists(ctx, db, "wmpf_sessions")
	if err != nil {
		return err
	}
	if !oldExists {
		return nil
	}
	newExists, err := sqliteTableExists(ctx, db, "sessions")
	if err != nil {
		return err
	}
	if !newExists {
		if _, err = db.ExecContext(ctx, "DROP INDEX IF EXISTS idx_sess_expires"); err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, "ALTER TABLE wmpf_sessions RENAME TO sessions")
		return err
	}
	if _, err = db.ExecContext(ctx, `
INSERT OR IGNORE INTO sessions
(id, wechat_account_id, uin, tcp_proxy, session_blob, expires_at, created_at, updated_at)
SELECT id, wechat_account_id, uin, tcp_proxy, session_blob, expires_at, created_at, updated_at
FROM wmpf_sessions`); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "DROP TABLE wmpf_sessions")
	return err
}

func sqliteTableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	return n > 0, err
}

func migrateAccountRemark(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(wechat_accounts)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "remark" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE wechat_accounts ADD COLUMN remark TEXT")
	return err
}

// migrateAccountOwnership 为老库补上账号归属（owner_user_id）与对外共享开关（api_shared）。
// 老数据 owner_user_id 为 NULL，表示未归属（等同管理员名下），普通用户看不到。
func migrateAccountOwnership(ctx context.Context, db *sql.DB) error {
	if err := ensureColumn(ctx, db, "wechat_accounts", "owner_user_id",
		"ALTER TABLE wechat_accounts ADD COLUMN owner_user_id INTEGER"); err != nil {
		return err
	}
	if err := ensureColumn(ctx, db, "wechat_accounts", "api_shared",
		"ALTER TABLE wechat_accounts ADD COLUMN api_shared INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_wxacc_owner ON wechat_accounts(owner_user_id)")
	return err
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, ddl string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, ddl)
	return err
}

// UpsertAccount 写入/更新账号。ownerUserID 为账号归属用户（nil 表示不改变已有归属）；
// 已有归属的账号在新扫描者带 ownerUserID 时归新扫描者所有（同一个人重新扫码即刷新归属）。
func (db *DB) UpsertAccount(ctx context.Context, openid, loginBuffer string, alias, nickname, avatar *string, userInfo map[string]any, credentials map[string]any, status *string, ownerUserID *int64) (*WechatAccount, error) {
	now := time.Now().Unix()
	userJSON, err := marshalNullable(userInfo)
	if err != nil {
		return nil, err
	}
	credJSON, err := marshalNullable(credentials)
	if err != nil {
		return nil, err
	}
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO wechat_accounts
		(openid, login_buffer, alias, nickname, avatar, user_info, credentials, status, owner_user_id, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(openid) DO UPDATE SET
		login_buffer=excluded.login_buffer, alias=excluded.alias, nickname=excluded.nickname,
		avatar=excluded.avatar, user_info=excluded.user_info, credentials=excluded.credentials,
		status=excluded.status, updated_at=excluded.updated_at,
		owner_user_id=CASE WHEN excluded.owner_user_id IS NULL
			THEN wechat_accounts.owner_user_id ELSE excluded.owner_user_id END`,
		openid, loginBuffer, nullableString(alias), nullableString(nickname), nullableString(avatar),
		userJSON, credJSON, nullableString(status), nullableInt(ownerUserID), now, now,
	)
	if err != nil {
		return nil, err
	}
	return db.GetAccountByOpenID(ctx, openid)
}

func (db *DB) GetAccount(ctx context.Context, id int64) (*WechatAccount, error) {
	return db.scanAccount(db.sql.QueryRowContext(ctx, selectAccountSQL+" WHERE id=?", id))
}

func (db *DB) GetAccountByOpenID(ctx context.Context, openid string) (*WechatAccount, error) {
	return db.scanAccount(db.sql.QueryRowContext(ctx, selectAccountSQL+" WHERE openid=?", openid))
}

func (db *DB) GetAccountByUIN(ctx context.Context, uin int64) (*WechatAccount, error) {
	return db.scanAccount(db.sql.QueryRowContext(ctx, selectAccountSQL+" WHERE uin=?", uin))
}

func (db *DB) ResolveAccount(ctx context.Context, ref string) (*WechatAccount, error) {
	if ref == "" {
		return nil, sql.ErrNoRows
	}
	if isDigits(ref) {
		n, _ := strconv.ParseInt(ref, 10, 64)
		if acc, err := db.GetAccountByUIN(ctx, n); err == nil {
			return acc, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return db.GetAccount(ctx, n)
	}
	return db.GetAccountByOpenID(ctx, ref)
}

func (db *DB) ListAccounts(ctx context.Context) ([]*WechatAccount, error) {
	rows, err := db.sql.QueryContext(ctx, selectAccountSQL+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WechatAccount
	for rows.Next() {
		acc, err := scanAccountRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

// ListAccountsOwnedBy 返回归属指定用户的账号（普通用户控制台只能看到这些）
func (db *DB) ListAccountsOwnedBy(ctx context.Context, ownerUserID int64) ([]*WechatAccount, error) {
	rows, err := db.sql.QueryContext(ctx, selectAccountSQL+" WHERE owner_user_id=? ORDER BY id", ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WechatAccount
	for rows.Next() {
		acc, err := scanAccountRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

// ListAccountsVisibleTo 返回控制台可见账号：all=true（管理员/未启用鉴权）返回全部，
// 否则只返回归属该用户的账号。
func (db *DB) ListAccountsVisibleTo(ctx context.Context, ownerUserID int64, all bool) ([]*WechatAccount, error) {
	if all {
		return db.ListAccounts(ctx)
	}
	return db.ListAccountsOwnedBy(ctx, ownerUserID)
}

// ListSharedAccounts 返回允许脚本/API 读取的账号（api_shared=1），供 /instances、
// 默认账号选择、保活等无会话路径使用。
func (db *DB) ListSharedAccounts(ctx context.Context) ([]*WechatAccount, error) {
	rows, err := db.sql.QueryContext(ctx, selectAccountSQL+" WHERE COALESCE(api_shared,1)<>0 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WechatAccount
	for rows.Next() {
		acc, err := scanAccountRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

// ErrAccountNotShared 表示账号存在但拥有者已关闭“允许脚本读取”。
var ErrAccountNotShared = errors.New("account not shared to api")

// ResolveSharedAccount 按 ref 解析账号，并要求该账号允许脚本/API 读取。
func (db *DB) ResolveSharedAccount(ctx context.Context, ref string) (*WechatAccount, error) {
	acc, err := db.ResolveAccount(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !acc.APIShared {
		return nil, ErrAccountNotShared
	}
	return acc, nil
}

// SetAccountShare 设置账号是否允许脚本/API 读取。
func (db *DB) SetAccountShare(ctx context.Context, id int64, shared bool) error {
	value := 0
	if shared {
		value = 1
	}
	_, err := db.sql.ExecContext(ctx,
		"UPDATE wechat_accounts SET api_shared=?, updated_at=? WHERE id=?",
		value, time.Now().Unix(), id,
	)
	return err
}

// SetAccountOwner 设置账号归属（nil 表示收归管理员/未归属）。
func (db *DB) SetAccountOwner(ctx context.Context, id int64, ownerUserID *int64) error {
	_, err := db.sql.ExecContext(ctx,
		"UPDATE wechat_accounts SET owner_user_id=?, updated_at=? WHERE id=?",
		nullableInt(ownerUserID), time.Now().Unix(), id,
	)
	return err
}

func (db *DB) SetAccountUIN(ctx context.Context, id, uin int64) error {
	_, err := db.sql.ExecContext(ctx, "UPDATE wechat_accounts SET uin=?, updated_at=? WHERE id=?", uin, time.Now().Unix(), id)
	return err
}

func (db *DB) SetAccountProfile(ctx context.Context, id int64, nickname, avatar *string, userInfo map[string]any) error {
	userJSON, err := marshalNullable(userInfo)
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx,
		"UPDATE wechat_accounts SET nickname=?, avatar=?, user_info=?, updated_at=? WHERE id=?",
		nullableString(nickname), nullableString(avatar), userJSON, time.Now().Unix(), id,
	)
	return err
}

func (db *DB) SetAccountRemark(ctx context.Context, id int64, remark string) error {
	remark = strings.TrimSpace(remark)
	var value any
	if remark != "" {
		value = remark
	}
	_, err := db.sql.ExecContext(ctx, "UPDATE wechat_accounts SET remark=?, updated_at=? WHERE id=?", value, time.Now().Unix(), id)
	return err
}

func (db *DB) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := db.sql.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key=?", key).Scan(&value)
	return value, err
}

func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := db.sql.ExecContext(ctx, `
INSERT INTO app_settings(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, key, value, time.Now().Unix())
	return err
}

func (db *DB) SetAccountCredential(ctx context.Context, id int64, loginBuffer string, credentials map[string]any) error {
	credJSON, err := marshalNullable(credentials)
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx,
		"UPDATE wechat_accounts SET login_buffer=?, credentials=?, updated_at=? WHERE id=?",
		loginBuffer, credJSON, time.Now().Unix(), id,
	)
	return err
}

func (db *DB) SetAccountCredentialStatus(ctx context.Context, id int64, loginBuffer string, credentials map[string]any, status string) error {
	credJSON, err := marshalNullable(credentials)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = db.sql.ExecContext(ctx,
		"UPDATE wechat_accounts SET login_buffer=?, credentials=?, status=?, last_checked_at=?, updated_at=? WHERE id=?",
		loginBuffer, credJSON, status, now, now, id,
	)
	return err
}

func (db *DB) SetAccountStatus(ctx context.Context, id int64, status string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx,
		"UPDATE wechat_accounts SET status=?, last_checked_at=?, updated_at=? WHERE id=?",
		status, now, now, id,
	)
	return err
}

func (db *DB) DeleteAccount(ctx context.Context, id int64) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM wechat_accounts WHERE id=?", id)
	return err
}

func (db *DB) GetSession(ctx context.Context, accountID int64, tcpProxy string) (*SessionRow, error) {
	row := db.sql.QueryRowContext(ctx,
		"SELECT id, wechat_account_id, uin, tcp_proxy, session_blob, expires_at, created_at, updated_at FROM sessions WHERE wechat_account_id=? AND tcp_proxy=? AND expires_at>?",
		accountID, tcpProxy, time.Now().Unix(),
	)
	return scanSession(row)
}

func (db *DB) PutSession(ctx context.Context, accountID int64, uin *int64, sessionBlob map[string]any, expiresAt int64, tcpProxy string) error {
	now := time.Now().Unix()
	blob, err := json.Marshal(sessionBlob)
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO sessions
		(wechat_account_id, uin, tcp_proxy, session_blob, expires_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(wechat_account_id, tcp_proxy) DO UPDATE SET
		uin=excluded.uin, session_blob=excluded.session_blob,
		expires_at=excluded.expires_at, updated_at=excluded.updated_at`,
		accountID, nullableInt(uin), tcpProxy, string(blob), expiresAt, now, now,
	)
	return err
}

func (db *DB) InvalidateSession(ctx context.Context, accountID int64, tcpProxy string) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM sessions WHERE wechat_account_id=? AND tcp_proxy=?", accountID, tcpProxy)
	return err
}

func (db *DB) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := db.sql.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<=?", time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (db *DB) ListFeatures(ctx context.Context, onlyEnabled bool) ([]Feature, error) {
	sqlText := "SELECT code, name, description, enabled FROM features"
	if onlyEnabled {
		sqlText += " WHERE enabled=1"
	}
	sqlText += " ORDER BY code"
	rows, err := db.sql.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (db *DB) ResolveFeature(ctx context.Context, ref any) (*Feature, error) {
	switch v := ref.(type) {
	case float64:
		return db.GetFeature(ctx, int(v))
	case int:
		return db.GetFeature(ctx, v)
	case string:
		if isDigits(v) {
			n, _ := strconv.Atoi(v)
			return db.GetFeature(ctx, n)
		}
		return db.GetFeatureByName(ctx, v)
	default:
		return nil, sql.ErrNoRows
	}
}

func (db *DB) GetFeature(ctx context.Context, code int) (*Feature, error) {
	row := db.sql.QueryRowContext(ctx, "SELECT code, name, description, enabled FROM features WHERE code=?", code)
	f, err := scanFeature(row)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (db *DB) GetFeatureByName(ctx context.Context, name string) (*Feature, error) {
	row := db.sql.QueryRowContext(ctx, "SELECT code, name, description, enabled FROM features WHERE name=? COLLATE NOCASE", name)
	f, err := scanFeature(row)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (a *WechatAccount) Public() AccountPublic {
	return AccountPublic{
		ID:            a.ID,
		OpenID:        a.OpenID,
		UIN:           a.UIN,
		Alias:         a.Alias,
		Nickname:      a.Nickname,
		Remark:        a.Remark,
		Avatar:        a.Avatar,
		Status:        a.Status,
		OwnerUserID:   a.OwnerUserID,
		APIShared:     a.APIShared,
		LastCheckedAt: a.LastCheckedAt,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}

// OwnedBy 判断账号是否归属指定用户（ownerUserID<=0 视为不限制）。
func (a *WechatAccount) OwnedBy(ownerUserID int64) bool {
	if ownerUserID <= 0 {
		return true
	}
	return a.OwnerUserID != nil && *a.OwnerUserID == ownerUserID
}

const selectAccountSQL = `SELECT id, openid, uin, alias, nickname, remark, avatar, user_info, login_buffer, credentials, status, last_checked_at, created_at, updated_at, owner_user_id, api_shared FROM wechat_accounts`

type accountScanner interface {
	Scan(dest ...any) error
}

type featureScanner interface {
	Scan(dest ...any) error
}

func (db *DB) scanAccount(row accountScanner) (*WechatAccount, error) {
	return scanAccountRows(row)
}

func scanAccountRows(row accountScanner) (*WechatAccount, error) {
	var (
		a                               WechatAccount
		uin, lastChecked, ownerID       sql.NullInt64
		apiShared                       sql.NullInt64
		alias, nickname, remark, avatar sql.NullString
		userJSON, credJSON              sql.NullString
		status                          sql.NullString
	)
	err := row.Scan(
		&a.ID, &a.OpenID, &uin, &alias, &nickname, &remark, &avatar, &userJSON,
		&a.LoginBuffer, &credJSON, &status, &lastChecked, &a.CreatedAt, &a.UpdatedAt,
		&ownerID, &apiShared,
	)
	if err != nil {
		return nil, err
	}
	if uin.Valid {
		a.UIN = &uin.Int64
	}
	if ownerID.Valid {
		a.OwnerUserID = &ownerID.Int64
	}
	a.APIShared = !apiShared.Valid || apiShared.Int64 != 0
	a.Alias = stringPtrFromNull(alias)
	a.Nickname = stringPtrFromNull(nickname)
	a.Remark = stringPtrFromNull(remark)
	a.Avatar = stringPtrFromNull(avatar)
	a.Status = stringPtrFromNull(status)
	if lastChecked.Valid {
		a.LastCheckedAt = &lastChecked.Int64
	}
	if userJSON.Valid && userJSON.String != "" {
		_ = json.Unmarshal([]byte(userJSON.String), &a.UserInfo)
	}
	if credJSON.Valid && credJSON.String != "" {
		_ = json.Unmarshal([]byte(credJSON.String), &a.Credentials)
	}
	return &a, nil
}

func scanSession(row accountScanner) (*SessionRow, error) {
	var s SessionRow
	var uin sql.NullInt64
	var blob string
	if err := row.Scan(&s.ID, &s.WechatAccountID, &uin, &s.TCPProxy, &blob, &s.ExpiresAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if uin.Valid {
		s.UIN = &uin.Int64
	}
	if err := json.Unmarshal([]byte(blob), &s.SessionBlob); err != nil {
		return nil, fmt.Errorf("decode session_blob: %w", err)
	}
	return &s, nil
}

func scanFeature(row featureScanner) (Feature, error) {
	var f Feature
	var desc sql.NullString
	var enabled int
	if err := row.Scan(&f.Code, &f.Name, &desc, &enabled); err != nil {
		return Feature{}, err
	}
	f.Description = stringPtrFromNull(desc)
	f.Enabled = enabled != 0
	return f, nil
}

func marshalNullable(v map[string]any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func nullableString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func nullableInt(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func stringPtrFromNull(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func stringPtr(s string) *string { return &s }

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
