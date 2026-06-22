package store

import "database/sql"

// rowScanner is satisfied by *sql.Row and *sql.Rows, letting the same scan
// helper work with both QueryRow and the per-row callback in collect.
type rowScanner interface {
	Scan(dest ...any) error
}

// collect drains rows into a slice using fn to scan each row.
func collect[T any](rows *sql.Rows, fn func(rowScanner) (*T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := fn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}
