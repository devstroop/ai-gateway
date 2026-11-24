package storage

import (
	"database/sql"
)

type RuntimeSettings struct {
	db *DB
}

func NewRuntimeSettings(db *DB) *RuntimeSettings {
	return &RuntimeSettings{db: db}
}

func (r *RuntimeSettings) Get(key string) (string, bool, error) {
	var v string
	err := r.db.QueryRow(`SELECT value FROM runtime_settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (r *RuntimeSettings) Set(key, value string) error {
	_, err := r.db.Exec(`INSERT INTO runtime_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (r *RuntimeSettings) Delete(key string) error {
	_, err := r.db.Exec(`DELETE FROM runtime_settings WHERE key = ?`, key)
	return err
}
