package pg

import (
	"context"
	_ "embed"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The two logins (§3.1): the workers' owns the schema, the API's reads it.
const (
	WorkerLogin = "aintercept_worker"
	APILogin    = "aintercept_api"
)

// loginsSQL creates the logins with their settings (logins.sql). It's written
// for psql (:'worker_password'), so Compose's initdb.sh and an operator run the
// same file.
//
//go:embed logins.sql
var loginsSQL string

// LoginsSQL is logins.sql with its passwords filled in, as psql would.
func LoginsSQL(workerPassword, apiPassword string) string {
	lit := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return strings.NewReplacer(":'worker_password'", lit(workerPassword), ":'api_password'", lit(apiPassword)).Replace(loginsSQL)
}

// CreateLogins runs logins.sql as an admin connected to the database the
// logins will use.
func CreateLogins(ctx context.Context, conn *pgx.Conn, workerPassword, apiPassword string) error {
	_, err := conn.Exec(ctx, LoginsSQL(workerPassword, apiPassword))
	return err
}
