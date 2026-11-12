package store

import (
	"context"
	"time"
)

type ProxyProviderProfile struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	ProxyType string `json:"proxy_type"`
	APIURL    string `json:"api_url"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (db *DB) ListProxyProviderProfiles(ctx context.Context) ([]*ProxyProviderProfile, error) {
	rows, err := db.sql.QueryContext(ctx, `
SELECT id, name, provider, proxy_type, api_url, created_at, updated_at
FROM proxy_provider_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]*ProxyProviderProfile, 0)
	for rows.Next() {
		profile := &ProxyProviderProfile{}
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.Provider, &profile.ProxyType, &profile.APIURL, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (db *DB) GetProxyProviderProfile(ctx context.Context, id int64) (*ProxyProviderProfile, error) {
	profile := &ProxyProviderProfile{}
	err := db.sql.QueryRowContext(ctx, `
SELECT id, name, provider, proxy_type, api_url, created_at, updated_at
FROM proxy_provider_profiles WHERE id=?`, id).Scan(
		&profile.ID, &profile.Name, &profile.Provider, &profile.ProxyType,
		&profile.APIURL, &profile.CreatedAt, &profile.UpdatedAt,
	)
	return profile, err
}

func (db *DB) CreateProxyProviderProfile(ctx context.Context, name, provider, proxyType, apiURL string) (*ProxyProviderProfile, error) {
	now := time.Now().Unix()
	result, err := db.sql.ExecContext(ctx, `
INSERT INTO proxy_provider_profiles(name, provider, proxy_type, api_url, created_at, updated_at)
VALUES(?,?,?,?,?,?)`, name, provider, proxyType, apiURL, now, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.GetProxyProviderProfile(ctx, id)
}

func (db *DB) UpdateProxyProviderProfile(ctx context.Context, id int64, name, provider, proxyType, apiURL string) (*ProxyProviderProfile, error) {
	_, err := db.sql.ExecContext(ctx, `
UPDATE proxy_provider_profiles SET name=?, provider=?, proxy_type=?, api_url=?, updated_at=? WHERE id=?`,
		name, provider, proxyType, apiURL, time.Now().Unix(), id,
	)
	if err != nil {
		return nil, err
	}
	return db.GetProxyProviderProfile(ctx, id)
}

func (db *DB) DeleteProxyProviderProfile(ctx context.Context, id int64) error {
	_, err := db.sql.ExecContext(ctx, "DELETE FROM proxy_provider_profiles WHERE id=?", id)
	return err
}
