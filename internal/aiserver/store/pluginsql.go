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

// PluginTablePrefix returns the unambiguous namespace a plugin's tables use.
//
// The doubled separator is deliberate: p_foo__ cannot be a prefix of
// p_foo_bar__, so one plugin name cannot shadow another. Plugin identifiers
// themselves must already consist of letters, digits, and underscores.
func PluginTablePrefix(plugin string) string {
	return "p_" + sanitizeIdentifier(plugin) + "__"
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
	ErrPluginSQLName        = errors.New("plugin name is not a safe SQL identifier")
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

// fromClausePattern isolates table-source lists. Comma joins need an explicit
// check because tableRefPattern only sees identifiers introduced by keywords.
// The filter is intentionally conservative: a complex FROM expression with a
// comma must use explicit JOIN syntax instead.
var fromClausePattern = regexp.MustCompile(
	`(?is)\bfrom\b(.*?)(?:\bwhere\b|\bjoin\b|\bgroup\s+by\b|\border\s+by\b|\blimit\b|\bunion\b|\bexcept\b|\bintersect\b|\breturning\b|$)`)

// ValidatePluginSQL checks a statement against the plugin sandbox rules.
//
// readOnly additionally requires a SELECT, which is what separates Query from
// Execute on the bridge.
func ValidatePluginSQL(plugin, query string, params int, readOnly bool) error {
	if !ValidPluginIdentifier(plugin) {
		return fmt.Errorf("%w: %q", ErrPluginSQLName, plugin)
	}
	if len(query) > maxPluginSQLLength {
		return ErrPluginSQLTooLong
	}
	if params > MaxPluginParams {
		return fmt.Errorf("%w: %d, limit is %d", ErrPluginSQLTooManyArgs, params, MaxPluginParams)
	}

	inspected, err := inspectPluginSQL(query)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(inspected)
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, ";"))
	if trimmed == "" {
		return ErrPluginSQLEmpty
	}

	// One statement per call. The lexical pass has already blanked literals
	// and comments, so only a real statement separator remains visible.
	if strings.Contains(trimmed, ";") {
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

	inspected = trimmed
	for _, clause := range fromClausePattern.FindAllStringSubmatch(inspected, -1) {
		if strings.Contains(clause[1], ",") {
			return fmt.Errorf("%w: comma-separated FROM sources require explicit JOIN", ErrPluginSQLForbidden)
		}
	}

	for _, match := range tableRefPattern.FindAllStringSubmatch(inspected, -1) {
		table := unquoteIdentifier(match[1])

		// A CTE or subquery alias is not a table. `FROM (SELECT ...)` produces
		// no identifier match at all, so only real names reach here.
		if strings.EqualFold(table, "if") || strings.EqualFold(table, "exists") {
			continue
		}
		if !ownsPluginTable(plugin, table) {
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

func ownsPluginTable(plugin, table string) bool {
	table = strings.ToLower(table)
	prefix := PluginTablePrefix(plugin)
	if suffix, ok := strings.CutPrefix(table, prefix); ok {
		return suffix != "" && sanitizeIdentifier(suffix) == suffix
	}

	// Compatibility for the pre-delimiter namespace. It is safe only for a
	// single-component table suffix; allowing underscores recreates the
	// p_foo_ / p_foo_bar_ prefix-confusion bug.
	legacy := "p_" + sanitizeIdentifier(plugin) + "_"
	suffix, ok := strings.CutPrefix(table, legacy)
	return ok && suffix != "" && !strings.Contains(suffix, "_") && sanitizeIdentifier(suffix) == suffix
}

// ValidPluginIdentifier reports whether a name has one stable SQL and
// filesystem spelling. Rejecting lossy sanitisation prevents two plugins from
// being mapped to the same namespace.
func ValidPluginIdentifier(name string) bool {
	return name != "" && name == strings.ToLower(name) && sanitizeIdentifier(name) == name
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

// inspectPluginSQL blanks string literals and comments in one lexical pass.
// Quoted identifiers remain visible to namespace checks. Crucially, comment
// markers inside literals and identifiers never change scanner state.
func inspectPluginSQL(s string) (string, error) {
	const (
		sqlNormal = iota
		sqlString
		sqlDoubleQuote
		sqlBacktick
		sqlBracket
		sqlLineComment
		sqlBlockComment
	)

	out := []byte(s)
	state := sqlNormal
	for i := 0; i < len(out); i++ {
		switch state {
		case sqlNormal:
			switch {
			case out[i] == '\'':
				out[i] = ' '
				state = sqlString
			case out[i] == '"':
				state = sqlDoubleQuote
			case out[i] == '`':
				state = sqlBacktick
			case out[i] == '[':
				state = sqlBracket
			case out[i] == '-' && i+1 < len(out) && out[i+1] == '-':
				out[i], out[i+1] = ' ', ' '
				i++
				state = sqlLineComment
			case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
				out[i], out[i+1] = ' ', ' '
				i++
				state = sqlBlockComment
			}
		case sqlString:
			if out[i] == '\'' {
				out[i] = ' '
				if i+1 < len(out) && out[i+1] == '\'' {
					out[i+1] = ' '
					i++
				} else {
					state = sqlNormal
				}
			} else {
				out[i] = ' '
			}
		case sqlDoubleQuote:
			if out[i] == '"' {
				if i+1 < len(out) && out[i+1] == '"' {
					i++
				} else {
					state = sqlNormal
				}
			}
		case sqlBacktick:
			if out[i] == '`' {
				if i+1 < len(out) && out[i+1] == '`' {
					i++
				} else {
					state = sqlNormal
				}
			}
		case sqlBracket:
			if out[i] == ']' {
				if i+1 < len(out) && out[i+1] == ']' {
					i++
				} else {
					state = sqlNormal
				}
			}
		case sqlLineComment:
			if out[i] == '\n' {
				state = sqlNormal
			} else {
				out[i] = ' '
			}
		case sqlBlockComment:
			if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = sqlNormal
			} else {
				out[i] = ' '
			}
		}
	}
	if state != sqlNormal && state != sqlLineComment {
		return "", fmt.Errorf("%w: unterminated quoted value, identifier, or comment", ErrPluginSQLForbidden)
	}
	return string(out), nil
}
