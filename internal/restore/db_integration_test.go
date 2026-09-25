package restore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
)

// This opt-in test uses a uniquely named login role and database, and removes
// both when done. CI runs it against a disposable PostgreSQL service.
func TestAnalyzePostgresIntegration(t *testing.T) {
	if os.Getenv("ANALYZE_INTEGRATION") != "1" {
		t.Skip("set ANALYZE_INTEGRATION=1 to run PostgreSQL integration test")
	}
	host, port, adminUser, adminPassword := os.Getenv("PGHOST"), os.Getenv("PGPORT"), os.Getenv("PGUSER"), os.Getenv("PGPASSWORD")
	if port == "" {
		port = "5432"
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	dbName, roleName, rolePassword := "analyze_test_"+suffix, "analyze_role_"+suffix, "analyze-test-password"
	admin := &config.Config{DBHost: host, DBPort: port, DBUser: adminUser, DBPass: adminPassword, DBName: "postgres"}
	app := &config.Config{DBHost: host, DBPort: port, DBUser: roleName, DBPass: rolePassword, DBName: dbName, DBAnalyzeTimeout: 30 * time.Second}
	adminTarget := *admin
	adminTarget.DBName = dbName
	psql := func(ctx context.Context, cfg *config.Config, sql string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "psql", "--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--host", cfg.DBHost, "--port", cfg.DBPort, "--username", cfg.DBUser, "--dbname", cfg.DBName, "--command", sql)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.DBPass)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	if _, err := psql(context.Background(), admin, "CREATE ROLE "+roleName+" LOGIN PASSWORD '"+rolePassword+"'"); err != nil {
		t.Fatalf("create isolated login role: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := psql(ctx, admin, "DROP DATABASE IF EXISTS "+dbName); err != nil {
			t.Errorf("drop isolated database: %v", err)
		}
		if _, err := psql(ctx, admin, "DROP ROLE IF EXISTS "+roleName); err != nil {
			t.Errorf("drop isolated role: %v", err)
		}
	})
	if _, err := psql(context.Background(), admin, "CREATE DATABASE "+dbName+" OWNER "+roleName); err != nil {
		t.Fatalf("create isolated database: %v", err)
	}

	fixtures := `
CREATE SCHEMA "app schema";
CREATE TABLE "app schema"."quoted""table" (value integer) WITH (autovacuum_enabled = false);
INSERT INTO "app schema"."quoted""table" SELECT generate_series(1, 1000);
CREATE SCHEMA pgcustom;
CREATE TABLE pgcustom.custom_table (value integer) WITH (autovacuum_enabled = false);
INSERT INTO pgcustom.custom_table SELECT generate_series(1, 1000);
CREATE TABLE public.plain_table (value integer) WITH (autovacuum_enabled = false);
INSERT INTO public.plain_table SELECT generate_series(1, 1000);
CREATE TABLE public.partitioned (value integer) PARTITION BY RANGE (value);
CREATE TABLE public.partitioned_part PARTITION OF public.partitioned FOR VALUES FROM (0) TO (2000)
  WITH (autovacuum_enabled = false);
INSERT INTO public.partitioned SELECT generate_series(1, 1000);
CREATE MATERIALIZED VIEW public.materialized WITH (autovacuum_enabled = false)
  AS SELECT value FROM public.plain_table;
`
	if _, err := psql(context.Background(), app, fixtures); err != nil {
		t.Fatalf("create fixtures as non-superuser owner: %v", err)
	}
	if got, err := psql(context.Background(), app, `SELECT count(*) FROM pg_stats WHERE schemaname IN ('public', 'app schema', 'pgcustom')`); err != nil || strings.TrimSpace(got) != "0" {
		t.Fatalf("expected no fixture statistics before Analyze, got %q (err %v)", got, err)
	}
	if err := newDatabase(nil).Analyze(context.Background(), app); err != nil {
		t.Fatalf("Analyze as non-superuser database owner: %v", err)
	}
	for _, table := range []string{"plain_table", "quoted\"table", "custom_table", "partitioned", "partitioned_part", "materialized"} {
		got, err := psql(context.Background(), app, fmt.Sprintf("SELECT count(*) FROM pg_stats WHERE tablename = '%s'", strings.ReplaceAll(table, "'", "''")))
		if err != nil || strings.TrimSpace(got) != "1" {
			t.Errorf("missing statistics for fixture %q: result %q, error %v", table, got, err)
		}
	}
	settings, err := psql(context.Background(), app, "SELECT current_setting('statement_timeout') || ',' || current_setting('lock_timeout')")
	if err != nil || strings.TrimSpace(settings) != "0,0" {
		t.Errorf("ANALYZE changed settings beyond its session: %q (err %v)", settings, err)
	}

	// A table owned by the admin is not analyzable by the application role.
	// Once the database ownership is also transferred away, PostgreSQL emits a
	// warning and skips it; Analyze must reject that partial result.
	if _, err := psql(context.Background(), &adminTarget, `CREATE TABLE public.admin_owned (private_value integer)`); err != nil {
		t.Fatalf("create inaccessible fixture: %v", err)
	}
	if _, err := psql(context.Background(), &adminTarget, "ALTER DATABASE "+quoteTestIdentifier(dbName)+" OWNER TO "+quoteTestIdentifier(adminUser)); err != nil {
		t.Fatalf("remove application database ownership: %v", err)
	}
	err = newDatabase(nil).Analyze(context.Background(), app)
	if err == nil {
		t.Fatal("Analyze succeeded despite an inaccessible application relation")
	}
	if !strings.Contains(err.Error(), "unexpected stderr diagnostics") {
		t.Fatalf("expected skipped-table warning rejection, got %v", err)
	}
	if strings.Contains(err.Error(), "admin_owned") || strings.Contains(err.Error(), "private_value") {
		t.Errorf("Analyze error exposed relation diagnostic: %v", err)
	}
}

func quoteTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
