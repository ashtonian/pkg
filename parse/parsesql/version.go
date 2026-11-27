package parsesql

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

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
// need: Have is the release the database reports (sqlite_version()), Need
// the oldest they run on (MinSQLiteVersion).
type VersionError struct {
	Have, Need string
}

func (e *VersionError) Error() string {
	return "parsesql: SQLite " + e.Have + " is older than " + e.Need + ", the oldest release parse's plans run on"
}

// CheckVersion refuses version, a SQLite release as sqlite_version()
// reports it, with a *VersionError when it is older than MinSQLiteVersion.
// New checks the database it opens a provider on.
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
		return nil, fmt.Errorf("parsesql: SQLite version %q is not major.minor.patch", version)
	}
	numbers := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("parsesql: SQLite version %q is not major.minor.patch", version)
		}
		numbers[i] = n
	}
	return numbers, nil
}

// libraryVersion is the release sqlite_version() reports on db. Every
// connection a *sql.DB opens comes from one driver, and so from one
// library, so one connection's answer is every connection's.
func libraryVersion(ctx context.Context, db *sql.DB) (string, error) {
	var version string
	err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version)
	if err != nil {
		return "", fmt.Errorf("parsesql: reading sqlite_version(): %w", err)
	}
	return version, nil
}
