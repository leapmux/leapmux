package mysql

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedTiDBServer answers the two queries enforceTiDBConstraintsOn asks and
// records every statement it ran, so a test can assert both what the check
// sent and what it concluded.
type scriptedTiDBServer struct {
	version     string
	versionErr  error
	setErr      error
	readValues  map[string]string
	readErr     error
	statements  []string
	scalarCalls []string
}

func (s *scriptedTiDBServer) server() tidbServer {
	return tidbServer{
		exec: func(_ context.Context, statement string) error {
			s.statements = append(s.statements, statement)
			return s.setErr
		},
		scalar: func(_ context.Context, query string) (string, error) {
			s.scalarCalls = append(s.scalarCalls, query)
			if query == "SELECT VERSION()" {
				return s.version, s.versionErr
			}
			if s.readErr != nil {
				return "", s.readErr
			}
			for name, value := range s.readValues {
				if strings.HasSuffix(query, name) {
					return value, nil
				}
			}
			return "OFF", nil
		},
	}
}

// capturedLog collects the level and message of every record, which is what
// each case below asserts on.
type capturedLog struct {
	records []slog.Record
}

func (c *capturedLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturedLog) Handle(_ context.Context, r slog.Record) error {
	c.records = append(c.records, r.Clone())
	return nil
}
func (c *capturedLog) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capturedLog) WithGroup(string) slog.Handler      { return c }

func (c *capturedLog) messagesAt(level slog.Level) []string {
	var out []string
	for _, r := range c.records {
		if r.Level == level {
			out = append(out, r.Message)
		}
	}
	return out
}

func runEnforcement(t *testing.T, s *scriptedTiDBServer) *capturedLog {
	t.Helper()
	log := &capturedLog{}
	enforceTiDBConstraintsOn(context.Background(), slog.New(log), s.server())
	return log
}

// Real MySQL has neither variable and answers "Unknown system variable", so
// the check must not run there at all -- an error log on every MySQL start
// would train an operator to ignore the one that matters.
func TestEnforceTiDBConstraintsSkipsRealMySQL(t *testing.T) {
	t.Parallel()

	s := &scriptedTiDBServer{version: "8.0.36"}
	log := runEnforcement(t, s)

	assert.Empty(t, s.statements, "a real MySQL server must receive no SET GLOBAL")
	assert.Empty(t, log.records, "a real MySQL server must produce no log at all")
}

// The happy path on TiDB: the check sets both variables, both read back ON, so
// the schema is enforced and the check reports nothing.
func TestEnforceTiDBConstraintsStaysQuietWhenEnforced(t *testing.T) {
	t.Parallel()

	s := &scriptedTiDBServer{
		version: "8.0.11-TiDB-v7.5.0",
		readValues: map[string]string{
			"tidb_enable_check_constraint": "ON",
			"tidb_enable_foreign_key":      "1",
		},
	}
	log := runEnforcement(t, s)

	assert.Len(t, s.statements, len(tidbEnforcementVariables),
		"every enforcement variable must be set")
	assert.Empty(t, log.records, "an enforced schema must produce no log")
}

// The case the finding is about: SET GLOBAL fails for want of
// SYSTEM_VARIABLES_ADMIN, the variable stays OFF, and the hub must SAY SO.
// The old code discarded the error and read nothing back, so every CHECK in
// the schema went inert in silence.
func TestEnforceTiDBConstraintsReportsAnUnenforcedSchema(t *testing.T) {
	t.Parallel()

	s := &scriptedTiDBServer{
		version: "8.0.11-TiDB-v7.5.0",
		setErr:  errors.New("access denied; you need SYSTEM_VARIABLES_ADMIN"),
		readValues: map[string]string{
			"tidb_enable_check_constraint": "OFF",
			"tidb_enable_foreign_key":      "OFF",
		},
	}
	log := runEnforcement(t, s)

	errs := log.messagesAt(slog.LevelError)
	require.NotEmpty(t, errs, "a refused SET GLOBAL must be reported, never discarded")
	assert.Contains(t, errs, "TiDB does not enforce part of the schema",
		"the read-back, not the write, is what decides the report")
	assert.Contains(t, errs, "could not turn on a TiDB constraint enforcement variable")
}

// A SET GLOBAL that SUCCEEDS is still not proof. A server can accept the
// statement and leave the variable off, which is why the check reads it back.
func TestEnforceTiDBConstraintsTrustsTheReadBackNotTheWrite(t *testing.T) {
	t.Parallel()

	s := &scriptedTiDBServer{
		version:    "8.0.11-TiDB-v8.1.0",
		readValues: map[string]string{"tidb_enable_check_constraint": "OFF", "tidb_enable_foreign_key": "ON"},
	}
	log := runEnforcement(t, s)

	errs := log.messagesAt(slog.LevelError)
	assert.Equal(t, []string{"TiDB does not enforce part of the schema"}, errs,
		"only the variable that read back OFF is reported")
}

// A read-back that fails leaves the hub unable to prove either answer, so it
// must report rather than assume the schema is enforced.
func TestEnforceTiDBConstraintsReportsAFailedReadBack(t *testing.T) {
	t.Parallel()

	s := &scriptedTiDBServer{version: "8.0.11-TiDB-v7.5.0", readErr: errors.New("unknown system variable")}
	log := runEnforcement(t, s)

	assert.Contains(t, log.messagesAt(slog.LevelError),
		"could not read back a TiDB constraint enforcement variable, so the schema may be unenforced")
}

// An unreadable version is not a reason to run SET GLOBAL blindly against a
// server that may be real MySQL, so the check reports and stops.
func TestEnforceTiDBConstraintsStopsWhenTheVersionIsUnreadable(t *testing.T) {
	t.Parallel()

	s := &scriptedTiDBServer{versionErr: errors.New("connection reset")}
	log := runEnforcement(t, s)

	assert.Empty(t, s.statements, "an unknown server must receive no SET GLOBAL")
	assert.Contains(t, log.messagesAt(slog.LevelWarn),
		"could not read the database server version, so the TiDB constraint check did not run")
}

func TestTiDBVariableIsOn(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"ON", "on", "1", " On "} {
		assert.True(t, tidbVariableIsOn(value), "%q means enforced", value)
	}
	for _, value := range []string{"OFF", "off", "0", "", "2"} {
		assert.False(t, tidbVariableIsOn(value), "%q does not mean enforced", value)
	}
}
