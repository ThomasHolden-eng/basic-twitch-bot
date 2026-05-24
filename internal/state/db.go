// Package state contains backend data state and access logic
package state

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

const (
	INTTABLEFILE = "state.db" // Database file string
	INTTABLE     = "state"    // Database table string
)

// KVStorage represents a key-value database.
//
// All write operations are performed sequentially, whereas
// all read operations are performed concurrently.
type KVStorage struct {
	writeDB *sql.DB // Database connection used for writing
	readDB  *sql.DB // Database connection used for reading
}

// NewKVStorage creates a new database connection.
func NewKVStorage() (*KVStorage, error) {
	dateSourceName := INTTABLEFILE + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"

	writeDB, err := sql.Open("sqlite", dateSourceName)
	if err != nil {
		return nil, err
	}
	readDB, err := sql.Open("sqlite", dateSourceName)
	if err != nil {
		return nil, err
	}

	writeDB.SetMaxOpenConns(1)

	query := `
CREATE TABLE IF NOT EXISTS ` + INTTABLE + ` (
key TEXT PRIMARY KEY,
value INTEGER
);`
	if _, err = writeDB.Exec(query); err != nil {
		return nil, err
	}

	return &KVStorage{writeDB: writeDB, readDB: readDB}, nil
}

// ReadInt returns the value of the specified key.
func (d *KVStorage) ReadInt(key string) (int, error) {
	var value int

	err := d.readDB.QueryRow("SELECT value FROM state WHERE key = ?", key).Scan(&value)
	if err != nil {
		return 0, err
	}

	return value, nil
}

// WriteInt sets the value of the specified key.
func (d *KVStorage) WriteInt(key string, value int) error {
	query := `
INSERT INTO ` + INTTABLE + ` (key, value) 
VALUES (?, ?) 
ON CONFLICT(key) DO UPDATE SET value = excluded.value;
`

	if _, err := d.writeDB.Exec(query, key, value); err != nil {
		return err
	}

	return nil
}

// IncrementInt adds the given increment to the value of the specified key.
// If the key does not exist, it initializes it with the increment.
func (d *KVStorage) IncrementInt(key string, increment int) error {
	query := `
INSERT INTO ` + INTTABLE + ` (key, value) 
VALUES (?, ?) 
ON CONFLICT(key) DO UPDATE SET value = ` + INTTABLE + `.value + excluded.value;
`

	if _, err := d.writeDB.Exec(query, key, increment); err != nil {
		return err
	}

	return nil
}

// Close closes the database connection.
func (d *KVStorage) Close() error {
	err := d.readDB.Close()
	if err != nil {
		return err
	}
	err = d.writeDB.Close()
	if err != nil {
		return err
	}
	return nil
}
