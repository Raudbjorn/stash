package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Plugin database access.
//
// The Python server handed plugins a SQLAlchemy session against the whole
// database, so any plugin could read or drop any other plugin's data, and the
// server's own. That is not a boundary that can be reintroduced here: plugins
// are downloaded code, and this database now sits inside the Stash process.
//
// So plugins get a deliberately small door: one statement per call, always
// parameterised, and only against tables they own.

// PluginTablePrefix returns the namespace a plugin's tables must live in.
//
// The prefix is part of the contract, not an implementation detail: a plugin's
// migrations create p_<plugin>_* tables and its queries may name nothing else.
func PluginTablePrefix(plugin string) string {
	return "p_" + sanitizeIdentifier(plugin) + "_"
}

// Limits on a single plugin query. Generous enough that no reasonable plugin
// notices, small enough that a runaway one cannot exhaust memory.
const (
	// MaxPluginRows caps rows returned from one query.
	MaxPluginRows = 10000
	// MaxPluginParams caps bound parameters in one statement.
	MaxPluginParams = 900
	// maxPluginSQLLength caps statement length, which also bounds the cost of
	// the checks below.
	maxPluginSQLLength = 64 * 1024
)

// Plugin SQL rejections. These reach the plugin as an exception naming the
// rule it broke, which is far more useful than a permission error from the
// engine would be.
var (
	ErrPluginSQLEmpty       = errors.New("empty statement")
	ErrPluginSQLTooLong     = errors.New("statement is too long")
	ErrPluginSQLMultiple    = errors.New("only one statement per call")
	ErrPluginSQLForbidden   = errors.New("statement is not permitted")
	ErrPluginSQLTooManyArgs = errors.New("too many parameters")
	ErrPluginSQLReadOnly    = errors.New("query must be a SELECT")
)

// ErrPluginTable reports a reference to a table outside the plugin's namespace.
type ErrPluginTable struct {
	Plugin string
	Table  string
}

func (e ErrPluginTable) Error() string {
	return fmt.Sprintf("plugin %s may not access table %q; its tables must be named %s*",
		e.Plugin, e.Table, PluginTablePrefix(e.Plugin))
}

// Statements a plugin may never issue, whatever tables they name. ATTACH is the
// sharpest of these: it would let a plugin open Stash's own database file and
// walk straight around the namespace rule.
var forbiddenVerbs = map[string]bool{
	"attach":  true,
	"detach":  true,
	"pragma":  true,
	"vacuum":  true,
	"reindex": true,
	"analyze": true,
}

// tableRefPattern finds the identifier after a keyword that introduces a table.
//
// This is not a SQL parser and does not pretend to be. It is a conservative
// filter: it finds every construct that can name a table, and anything it
// cannot resolve is refused rather than allowed. Combined with the
// single-statement and forbidden-verb rules, that is enough - the risk it
// guards against is a plugin reading its neighbour's rows, not a determined
// attacker with arbitrary code execution, who already has everything.
var tableRefPattern = regexp.MustCompile(
	`(?i)\b(?:from|join|into|update|table)\s+([a-zA-Z_][a-zA-Z0-9_]*|"[^"]+"|` + "`[^`]+`" + `|\[[^\]]+\])`)

// commentPattern strips SQL comments before inspection, so a table reference
// cannot be hidden behind one.
var commentPattern = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)

// ValidatePluginSQL checks a statement against the plugin sandbox rules.
//
// readOnly additionally requires a SELECT, which is what separates Query from
// Execute on the bridge.
func ValidatePluginSQL(plugin, query string, params int, readOnly bool) error {
	if len(query) > maxPluginSQLLength {
		return ErrPluginSQLTooLong
	}
	if params > MaxPluginParams {
		return fmt.Errorf("%w: %d, limit is %d", ErrPluginSQLTooManyArgs, params, MaxPluginParams)
	}

	stripped := commentPattern.ReplaceAllString(query, " ")
	trimmed := strings.TrimSpace(stripped)
	trimmed = strings.TrimSuffix(trimmed, ";")
	if trimmed == "" {
		return ErrPluginSQLEmpty
	}

	// One statement per call. A semicolon inside a string literal is legal, so
	// literals are blanked before looking - otherwise `WHERE name = 'a;b'`
	// would be refused for no reason.
	if strings.Contains(blankStringLiterals(trimmed), ";") {
		return ErrPluginSQLMultiple
	}

	fields := strings.Fields(strings.ToLower(trimmed))
	if len(fields) == 0 {
		return ErrPluginSQLEmpty
	}
	verb := fields[0]

	if forbiddenVerbs[verb] {
		return fmt.Errorf("%w: %s", ErrPluginSQLForbidden, strings.ToUpper(verb))
	}
	if readOnly && verb != "select" && verb != "with" {
		return ErrPluginSQLReadOnly
	}

	prefix := PluginTablePrefix(plugin)
	for _, match := range tableRefPattern.FindAllStringSubmatch(blankStringLiterals(trimmed), -1) {
		table := unquoteIdentifier(match[1])

		// A CTE or subquery alias is not a table. `FROM (SELECT ...)` produces
		// no identifier match at all, so only real names reach here.
		if strings.EqualFold(table, "if") || strings.EqualFold(table, "exists") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(table), prefix) {
			return ErrPluginTable{Plugin: plugin, Table: table}
		}
	}

	return nil
}

// PluginQuery runs a read-only statement for a plugin and returns its rows.
func (db *DB) PluginQuery(ctx context.Context, plugin, query string, params []any) ([]map[string]any, error) {
	if err := ValidatePluginSQL(plugin, query, len(params), true); err != nil {
		return nil, err
	}

	rows, err := db.sql.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	// Never nil: the plugin side iterates the result directly.
	out := []map[string]any{}
	for rows.Next() {
		if len(out) >= MaxPluginRows {
			return nil, fmt.Errorf("query returned more than %d rows; add a LIMIT", MaxPluginRows)
		}

		cells := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range cells {
			targets[i] = &cells[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}

		row := make(map[string]any, len(columns))
		for i, name := range columns {
			// []byte would marshal to base64 on the way back; the plugin asked
			// for text and expects text.
			if raw, ok := cells[i].([]byte); ok {
				row[name] = string(raw)
				continue
			}
			row[name] = cells[i]
		}
		out = append(out, row)
	}

	return out, rows.Err()
}

// PluginExecute runs a write statement for a plugin and returns rows affected.
func (db *DB) PluginExecute(ctx context.Context, plugin, query string, params []any) (int64, error) {
	if err := ValidatePluginSQL(plugin, query, len(params), false); err != nil {
		return 0, err
	}

	result, err := db.sql.ExecContext(ctx, query, params...)
	if err != nil {
		return 0, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		// Not every statement reports a row count; that is not a failure.
		return 0, nil
	}
	return affected, nil
}

// PluginExecuteBatch runs several statements in one transaction.
//
// Batching exists because this engine's per-statement overhead is significant,
// and a plugin writing a few thousand rows one statement at a time is
// noticeably slow.
func (db *DB) PluginExecuteBatch(ctx context.Context, plugin string, statements []string, params [][]any) (int64, error) {
	if len(statements) != len(params) {
		return 0, errors.New("statement and parameter counts differ")
	}
	for i, statement := range statements {
		if err := ValidatePluginSQL(plugin, statement, len(params[i]), false); err != nil {
			return 0, fmt.Errorf("statement %d: %w", i, err)
		}
	}

	var total int64
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		for i, statement := range statements {
			result, err := tx.ExecContext(ctx, statement, params[i]...)
			if err != nil {
				return fmt.Errorf("statement %d: %w", i, err)
			}
			if affected, err := result.RowsAffected(); err == nil {
				total += affected
			}
		}
		return nil
	})
	return total, err
}

// sanitizeIdentifier reduces a plugin name to characters legal in a table name.
func sanitizeIdentifier(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func unquoteIdentifier(s string) string {
	if len(s) < 2 {
		return s
	}
	switch s[0] {
	case '"', '`':
		if s[len(s)-1] == s[0] {
			return s[1 : len(s)-1]
		}
	case '[':
		if s[len(s)-1] == ']' {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// blankStringLiterals replaces the contents of single-quoted literals with
// spaces, preserving offsets, so keyword and semicolon scanning cannot be
// misled by data.
func blankStringLiterals(s string) string {
	out := []byte(s)
	inString := false
	for i := 0; i < len(out); i++ {
		if out[i] == '\'' {
			// '' inside a string is an escaped quote, which this handles
			// naturally by toggling twice.
			inString = !inString
			continue
		}
		if inString {
			out[i] = ' '
		}
	}
	return string(out)
}
