package sqlutil

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
)

// TableHoldsText searches stored text and binary cells for test assertions.
// Only application tables from the fixed table list can enter the query.
func TableHoldsText(ctx context.Context, db DBQuery, table, text string) (bool, error) {
	if !slices.Contains(SQLTruncateTableOrder, table) {
		return false, fmt.Errorf("unknown application table %q", table)
	}
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+table)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	needle := []byte(text)
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			return false, err
		}
		for _, value := range values {
			switch value := value.(type) {
			case string:
				if strings.Contains(value, text) {
					return true, nil
				}
			case []byte:
				if bytes.Contains(value, needle) {
					return true, nil
				}
			}
		}
	}
	return false, rows.Err()
}
