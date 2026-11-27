package parsesqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"modernc.org/sqlite"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// MinSQLiteVersion is the oldest SQLite release parse's plans run on: the
// first whose JSON functions read every member name a plan's path quotes
// as itself. From 3.45.0 they compare a label with a member's name as
// decoded, and from 3.47.0 they read an escaped double quote inside a
// quoted label, where an older release ends the label there and json_set
// writes other members.
const MinSQLiteVersion = dialect.SQLiteMinVersion

// VersionError is a SQLite library older than the release parse's plans
// need: Have is the release the connection reports (sqlite_version()),
// Need the oldest they run on (MinSQLiteVersion).
type VersionError struct {
	Have, Need string
}

func (e *VersionError) Error() string {
	return "parsesqlite: SQLite " + e.Have + " is older than " + e.Need + ", the oldest release parse's plans run on"
}

// CheckVersion refuses version, a SQLite release as sqlite_version()
// reports it, with a *VersionError when it is older than MinSQLiteVersion.
// Register and RegisterOn check every connection modernc.org/sqlite opens;
// another driver calls CheckVersion from a connection hook of its own.
func CheckVersion(version string) error {
	return checkVersion(version, MinSQLiteVersion)
}

func checkVersion(have, need string) error {
	h, err := releaseNumbers(have)
	if err != nil {
		return err
	}
	n, err := releaseNumbers(need)
	if err != nil {
		return err
	}
	if slices.Compare(h, n) < 0 {
		vErr := &VersionError{Have: have, Need: need}
		return vErr
	}
	return nil
}

// releaseNumbers is a SQLite release, major.minor.patch, as its numbers.
func releaseNumbers(version string) ([]int, error) {
	fields := strings.Split(version, ".")
	if len(fields) != 3 {
		return nil, fmt.Errorf("parsesqlite: SQLite version %q is not major.minor.patch", version)
	}
	numbers := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("parsesqlite: SQLite version %q is not major.minor.patch", version)
		}
		numbers[i] = n
	}
	return numbers, nil
}

// versionHook refuses a connection whose SQLite is older than need.
func versionHook(need string) sqlite.ConnectionHookFn {
	return func(conn sqlite.ExecQuerierContext, _ string) error {
		have, err := libraryVersion(conn)
		if err != nil {
			return err
		}
		return checkVersion(have, need)
	}
}

// libraryVersion is what sqlite_version() reports on conn. A connection
// hook is given no context; the query runs in process on a connection
// nothing else holds yet.
func libraryVersion(conn sqlite.ExecQuerierContext) (string, error) {
	rows, err := conn.QueryContext(context.Background(), "SELECT sqlite_version()", nil)
	if err != nil {
		return "", fmt.Errorf("parsesqlite: reading sqlite_version(): %w", err)
	}
	row := make([]driver.Value, 1)
	err = errors.Join(rows.Next(row), rows.Close())
	if err != nil {
		return "", fmt.Errorf("parsesqlite: reading sqlite_version(): %w", err)
	}
	version, ok := row[0].(string)
	if !ok {
		return "", fmt.Errorf("parsesqlite: sqlite_version() returned %s, not text", gowalker.TypeText(reflect.TypeOf(row[0])))
	}
	return version, nil
}
