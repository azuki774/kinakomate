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

// database restores the staged gzip dump into PostgreSQL using psql. The
// connection inputs come from Config; the command-injection point (psql
// execution) is swappable so tests can observe the invocation without a real
// database.
type database struct {
	logger *slog.Logger
	// runPsql executes psql with the given SQL reader on stdin. It returns the
	// psql stderr (for error reporting) and any error. If nil, the default
	// implementation builds a real psql command.
	runPsql func(ctx context.Context, cfg *config.Config, stdin io.Reader) (string, error)
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

// CheckConnection verifies the runner can reach the target database. It runs
// psql with a no-op connection-only command (`SELECT 1`) to confirm the server
// is reachable and the credentials are accepted.
func (d *database) CheckConnection(ctx context.Context, cfg *config.Config) error {
	stderr, err := d.exec(ctx, cfg, "SELECT 1", nil)
	if err != nil {
		return fmt.Errorf("database connection check failed: %w: %s", err, strings.TrimSpace(stderr))
	}
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
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer zr.Close()

	// psql reads the SQL from stdin; --set ON_ERROR_STOP=1 + --single-transaction
	// make it stop and ROLLBACK the whole dump on the first error. The gzip
	// stream is expanded on the fly and never written to disk.
	stderr, err := d.exec(ctx, cfg, "", zr)
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

// exec runs psql for either a connection check or a restore. When stdin is
// non-nil it is streamed to psql over stdin; when it is nil the given command
// text is passed via -c. The database password is passed through PGPASSWORD in
// the environment so it never appears in the psql arguments or logs.
func (d *database) exec(ctx context.Context, cfg *config.Config, command string, stdin io.Reader) (string, error) {
	if d.runPsql != nil {
		return d.runPsql(ctx, cfg, stdin)
	}
	return psqlRestore(ctx, cfg, command, stdin)
}

// psqlRestore is the default psql command runner.
func psqlRestore(ctx context.Context, cfg *config.Config, command string, stdin io.Reader) (string, error) {
	cmd := buildPsqlCmd(ctx, cfg, command, stdin)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stderr.String(), err
	}
	return stderr.String(), nil
}

// buildPsqlCmd constructs the psql command for the given config. A non-nil
// stdin is streamed to psql over stdin; a nil stdin runs the given command text
// via --command. The database password is passed through PGPASSWORD in the
// environment so it never appears in the psql arguments or logs.
func buildPsqlCmd(ctx context.Context, cfg *config.Config, command string, stdin io.Reader) *exec.Cmd {
	args := []string{
		"--set", "ON_ERROR_STOP=1",
		"--single-transaction",
		"--quiet",
		"--host", cfg.DBHost,
		"--port", cfg.DBPort,
		"--username", cfg.DBUser,
		"--dbname", cfg.DBName,
	}
	if stdin == nil {
		args = append(args, "--command", command)
	}

	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.DBPass)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	return cmd
}
