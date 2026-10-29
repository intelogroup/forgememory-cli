package db

import (
	"database/sql"
	"errors"
)

// ErrTxRequired is returned by the *Tx helpers when a nil transaction is
// passed, so a caller that forgot to start one fails loudly instead of
// silently writing outside its transaction.
var ErrTxRequired = errors.New("db: transaction required")

// connOrTx narrows the writers these helpers need to either the pool or an
// open transaction. A nil *connOrTx means "run on the pool".
type connOrTx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func execOn(pool *sql.DB, c *connOrTx) func(string, ...any) (sql.Result, error) {
	if c != nil {
		return (*c).Exec
	}
	return pool.Exec
}

func queryRowOn(pool *sql.DB, c *connOrTx) func(string, ...any) *sql.Row {
	if c != nil {
		return (*c).QueryRow
	}
	return pool.QueryRow
}

func queryOn(pool *sql.DB, c *connOrTx) func(string, ...any) (*sql.Rows, error) {
	if c != nil {
		return (*c).Query
	}
	return pool.Query
}

// WithTx runs fn inside a transaction and commits only when fn returns nil.
// Every consolidation write for one batch goes through here, so a failure
// halfway through leaves the batch's earlier ops unapplied rather than
// committed.
func (d *DB) WithTx(fn func(tx *sql.Tx) error) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
