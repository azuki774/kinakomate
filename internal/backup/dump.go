package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/azuki774/kinakomate/internal/config"
)

var pgDumpCommand = exec.CommandContext

func buildPgDumpCommand(ctx context.Context, cfg *config.BackupConfig) *exec.Cmd {
	args := []string{
		"--format=plain",
		"--no-owner",
		"--no-acl",
		"--host=" + cfg.DBHost,
		"--port=" + cfg.DBPort,
		"--username=" + cfg.DBUser,
		"--dbname=" + databaseNameConninfo(cfg.DBName),
	}
	cmd := pgDumpCommand(ctx, "pg_dump", args...)
	cmd.Env = pgDumpEnvironment(cfg.DBPass)
	cmd.Stderr = io.Discard
	return cmd
}

func databaseNameConninfo(name string) string {
	var conninfo strings.Builder
	conninfo.Grow(len(name) + len("dbname=''"))
	conninfo.WriteString("dbname='")
	for i := range len(name) {
		if name[i] == '\\' || name[i] == '\'' {
			conninfo.WriteByte('\\')
		}
		conninfo.WriteByte(name[i])
	}
	conninfo.WriteByte('\'')
	return conninfo.String()
}

func pgDumpEnvironment(password string) []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key == "PGPASSWORD" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "PGPASSWORD="+password)
}

func dump(ctx context.Context, cfg *config.BackupConfig, dst io.Writer) error {
	cmd := buildPgDumpCommand(ctx, cfg)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("unable to capture pg_dump output")
	}
	if err := cmd.Start(); err != nil {
		return errors.New("unable to start pg_dump")
	}

	compressed := gzip.NewWriter(dst)
	_, copyErr := io.Copy(compressed, stdout)
	if copyErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	gzipErr := compressed.Close()
	if copyErr != nil || waitErr != nil || gzipErr != nil {
		return errors.New("pg_dump or gzip output failed")
	}
	return nil
}
