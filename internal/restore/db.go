package restore

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/azuki774/kinakomate/internal/config"
)

// adminDBName is the maintenance database used for the connection check and
// for DROP/CREATE DATABASE. The target database itself cannot host these
// statements: it may not exist yet, and database DDL cannot run while
// connected to the database being dropped.
const adminDBName = "postgres"

// psqlInvocation describes one psql execution. DBName selects the database to
// connect to. When Stdin is non-nil the SQL is streamed via --file=-,
// otherwise Command runs via --command. SingleTransaction adds
// --single-transaction (used only for the dump restore).
type psqlInvocation struct {
	DBName            string
	Command           string
	Stdin             io.Reader
	SingleTransaction bool
}

// database restores the staged gzip dump into PostgreSQL using psql. The
// connection inputs come from Config; the command-injection point (psql
// execution) is swappable so tests can observe the invocation without a real
// database.
type database struct {
	logger *slog.Logger
	// runPsql executes a single psql invocation. It returns the psql stderr
	// (for error reporting) and any error. If nil, the default implementation
	// builds a real psql command.
	runPsql func(ctx context.Context, cfg *config.Config, inv psqlInvocation) (string, error)
}

// newDatabase builds a database with the default psql command runner.
func newDatabase() *database {
	return &database{
		logger: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
}

// log returns the configured logger or a default one so a database built
// without a logger (as in tests) never panics.
func (d *database) log() *slog.Logger {
	if d.logger == nil {
		return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return d.logger
}

// CheckConnection verifies the runner can reach the PostgreSQL server. It
// connects to the maintenance database, not the target, so the check also
// passes when the target database is missing (e.g. after a previous failed
// run left it dropped).
func (d *database) CheckConnection(ctx context.Context, cfg *config.Config) error {
	stderr, err := d.exec(ctx, cfg, psqlInvocation{DBName: adminDBName, Command: "SELECT 1"})
	if err != nil {
		return fmt.Errorf("database connection check failed: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Reset recreates the target database from template0 so the restore always
// starts from an empty database. A plain SQL dump does not clean the target,
// so leftover objects from a previous run would collide with the dump (e.g.
// duplicate CREATE TYPE). The admin statements run against the maintenance
// database, outside any transaction (database DDL cannot run inside one),
// terminating stray backend sessions first. The recreated database is owned
// by DB_USER, the role running the statements.
func (d *database) Reset(ctx context.Context, cfg *config.Config) error {
	terminate := fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid();",
		cfg.DBName,
	)
	drop := fmt.Sprintf("DROP DATABASE IF EXISTS %s;", cfg.DBName)
	create := fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0;", cfg.DBName)
	for _, stmt := range []string{terminate, drop, create} {
		stderr, err := d.exec(ctx, cfg, psqlInvocation{DBName: adminDBName, Command: stmt})
		if err != nil {
			return fmt.Errorf("database reset failed: %w: %s", err, strings.TrimSpace(stderr))
		}
	}

	d.log().InfoContext(ctx, "database reset completed",
		"db_host", cfg.DBHost,
		"db_name", cfg.DBName,
	)
	return nil
}

// Restore streams the gzip dump from disk to psql's stdin and applies it as a
// single transaction with ON_ERROR_STOP, aborts the transaction on the first
// error (psql emits ROLLBACK), and treats the step as failed — never as a
// partially-succeeded restore.
func (d *database) Restore(ctx context.Context, cfg *config.Config, dump *Dump) error {
	f, err := os.Open(dump.Path)
	if err != nil {
		return fmt.Errorf("open staged gzip dump: %w", err)
	}
	defer f.Close() //nolint:errcheck

	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer zr.Close() //nolint:errcheck

	// psql reads the SQL from stdin; --set ON_ERROR_STOP=1 + --single-transaction
	// make it stop and ROLLBACK the whole dump on the first error. The gzip
	// stream is expanded on the fly and never written to disk.
	stderr, err := d.exec(ctx, cfg, psqlInvocation{
		DBName:            cfg.DBName,
		Stdin:             zr,
		SingleTransaction: true,
	})
	if err != nil {
		return fmt.Errorf("database restore failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	d.log().InfoContext(ctx, "database restore completed",
		"db_host", cfg.DBHost,
		"db_name", cfg.DBName,
		"dump_s3_bucket", dump.Bucket,
		"dump_s3_key", dump.Key,
		"dump_etag", dump.ETag,
		"dump_size", dump.Size,
	)
	return nil
}

// exec runs a single psql invocation. The database password is passed through
// PGPASSWORD in the environment so it never appears in the psql arguments or
// logs.
func (d *database) exec(ctx context.Context, cfg *config.Config, inv psqlInvocation) (string, error) {
	if d.runPsql != nil {
		return d.runPsql(ctx, cfg, inv)
	}
	return psqlRun(ctx, cfg, inv)
}

// psqlRun is the default psql command runner.
func psqlRun(ctx context.Context, cfg *config.Config, inv psqlInvocation) (string, error) {
	cmd := buildPsqlCmd(ctx, cfg, inv)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stderr.String(), err
	}
	return stderr.String(), nil
}

// buildPsqlCmd constructs the psql command for the given invocation. The
// database password is passed through PGPASSWORD in the environment so it
// never appears in the psql arguments or logs.
func buildPsqlCmd(ctx context.Context, cfg *config.Config, inv psqlInvocation) *exec.Cmd {
	args := []string{
		"--set", "ON_ERROR_STOP=1",
		"--no-psqlrc",
		"--quiet",
		"--host", cfg.DBHost,
		"--port", cfg.DBPort,
		"--username", cfg.DBUser,
		"--dbname", inv.DBName,
	}
	if inv.Stdin != nil {
		args = append(args, "--file", "-")
	} else {
		args = append(args, "--command", inv.Command)
	}
	if inv.SingleTransaction {
		args = append(args, "--single-transaction")
	}

	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.DBPass)
	if inv.Stdin != nil {
		cmd.Stdin = inv.Stdin
	}
	return cmd
}
