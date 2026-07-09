// Package state contains backend data state and access logic
package state

import (
	"database/sql"
	"errors"

	_ "modernc.org/sqlite"
)

const (
	defaultDBPath = "state.db" // Default SQLite file path
	TABLE         = "state"    // Database table string
	INTCOL        = "val_int"  // Database column string
	STRCOL        = "val_str"  // Database column string
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
//
// Pass an empty string to use the default file ("state.db"). Pass
// ":memory:" for an in-memory database, useful in tests.
func NewKVStorage(path string) (*KVStorage, error) {
	if path == "" {
		path = defaultDBPath
	}
	dateSourceName := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"

	writeDB, err := sql.Open("sqlite", dateSourceName)
	if err != nil {
		return nil, err
	}
	readDB, err := sql.Open("sqlite", dateSourceName)
	if err != nil {
		writeDB.Close()
		return nil, err
	}

	writeDB.SetMaxOpenConns(1)

	query := `
CREATE TABLE IF NOT EXISTS ` + TABLE + ` (
key TEXT PRIMARY KEY,
` + INTCOL + ` INTEGER NOT NULL DEFAULT 0,
` + STRCOL + ` TEXT NOT NULL DEFAULT ''
);`
	if _, err = writeDB.Exec(query); err != nil {
		return nil, err
	}

	return &KVStorage{writeDB: writeDB, readDB: readDB}, nil
}

// ReadInt returns the integer value of the specified key.
func (d *KVStorage) ReadInt(key string) (int, error) {
	value, err := d.read(key, INTCOL)
	if err != nil {
		return 0, err
	}

	return int(value.(int64)), err
}

// ReadString returns the string value of the specified key.
func (d *KVStorage) ReadString(key string) (string, error) {
	value, err := d.read(key, STRCOL)
	if err != nil {
		return "", err
	}

	return string(value.(string)), err
}

// WriteInt sets the integer value of the specified key.
func (d *KVStorage) WriteInt(key string, value int) error {
	return d.write(key, INTCOL, value)
}

// WriteString sets the string value of the specified key.
func (d *KVStorage) WriteString(key string, value string) error {
	return d.write(key, STRCOL, value)
}

// IncrementInt adds the given increment to the value of the specified key.
// If the key does not exist, it initializes it with the increment.
func (d *KVStorage) IncrementInt(key string, increment int) error {
	return d.upsert(key, INTCOL, increment, TABLE+`.`+INTCOL+` + excluded.`+INTCOL)
}

// Close closes the database connection.
func (d *KVStorage) Close() error {
	return errors.Join(d.readDB.Close(), d.writeDB.Close())
}

// read returns the value of the specified column for the specified key.
func (d *KVStorage) read(key, column string) (any, error) {
	var value any

	err := d.readDB.QueryRow(
		"SELECT "+column+" FROM "+TABLE+" WHERE key = ?", key,
	).Scan(&value)
	if err != nil {
		return nil, err
	}

	return value, nil
}

// write sets the value of the specified column for the specified key.
func (d *KVStorage) write(key, column string, value any) error {
	return d.upsert(key, column, value, "excluded."+column)
}

// upsert sets the value of the specified column for the specified key using the given expression.
func (d *KVStorage) upsert(key, column string, value any, expression string) error {
	query := `
INSERT INTO ` + TABLE + ` (key, ` + column + `) 
VALUES (?, ?) 
ON CONFLICT(key) DO UPDATE SET ` + column + ` = ` + expression + `;
`
	if _, err := d.writeDB.Exec(query, key, value); err != nil {
		return err
	}

	return nil
}
