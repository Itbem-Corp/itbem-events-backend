package configuration

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

type closeTrackingDriver struct {
	closed chan struct{}
}

func (d *closeTrackingDriver) Open(string) (driver.Conn, error) {
	return &closeTrackingConn{closed: d.closed}, nil
}

type closeTrackingConn struct {
	closed chan struct{}
	once   sync.Once
}

func (*closeTrackingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *closeTrackingConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (*closeTrackingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func TestPublicMomentsWallIndexIsConcurrentPartialAndOrdered(t *testing.T) {
	normalized := strings.Join(strings.Fields(publicMomentsWallIndexDDL), " ")

	required := []string{
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_moments_public_wall_v1",
		`(CASE WHEN "order" > 0 THEN 0 ELSE 1 END) ASC`,
		`"order" ASC`,
		"created_at DESC",
		"id DESC",
		"WHERE deleted_at IS NULL",
		"is_approved = true",
		"processing_status IN ('', 'done')",
	}
	for _, fragment := range required {
		if !strings.Contains(normalized, fragment) {
			t.Errorf("public moments wall index is missing %q: %s", fragment, normalized)
		}
	}

	orderedKeys := []string{
		"event_id",
		`(CASE WHEN "order" > 0 THEN 0 ELSE 1 END) ASC`,
		`"order" ASC`,
		"created_at DESC",
		"id DESC",
	}
	previous := -1
	for _, key := range orderedKeys {
		position := strings.Index(normalized, key)
		if position <= previous {
			t.Fatalf("index key %q is missing or out of order in %s", key, normalized)
		}
		previous = position
	}
}

func TestPublicMomentsWallIndexUsesVersionedNameForSafeEvolution(t *testing.T) {
	if !strings.HasSuffix(publicMomentsWallIndexName, "_v1") {
		t.Fatalf("performance index name must be versioned, got %q", publicMomentsWallIndexName)
	}
	if !strings.Contains(publicMomentsWallIndexDDL, publicMomentsWallIndexName) {
		t.Fatalf("index DDL does not use %q", publicMomentsWallIndexName)
	}
	if !strings.Contains(publicMomentsWallIndexDropDDL, publicMomentsWallIndexName) {
		t.Fatalf("index cleanup DDL does not use %q", publicMomentsWallIndexName)
	}
}

func TestEnsurePerformanceIndexesRejectsNilDatabase(t *testing.T) {
	if err := EnsurePerformanceIndexes(nil); err == nil {
		t.Fatal("expected nil database to be rejected")
	}
}

func TestDiscardSQLConnectionClosesPhysicalDriverSession(t *testing.T) {
	closed := make(chan struct{})
	const driverName = "eventi-postgres-index-discard-test"
	sql.Register(driverName, &closeTrackingDriver{closed: closed})

	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open tracking database: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve tracking connection: %v", err)
	}

	if err := discardSQLConnection(conn); err != nil {
		t.Fatalf("discard SQL connection: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("discard did not close the physical driver session")
	}
	if err := conn.PingContext(context.Background()); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("discarded sql.Conn remained usable: %v", err)
	}
}

func TestNormalizeIndexPredicateAcceptsPostgresCanonicalCastsOnly(t *testing.T) {
	canonical := `deleted_at IS NULL AND is_approved = true AND
		(processing_status::text = ANY (ARRAY[''::character varying, 'done'::character varying]::text[]))`
	want := `deleted_atisnullandis_approved=trueandprocessing_status=anyarray['','done']`
	if got := normalizeIndexPredicate(canonical); got != want {
		t.Fatalf("unexpected normalized predicate: got %q want %q", got, want)
	}

	wrong := canonical + ` OR processing_status = 'failed'`
	if normalizeIndexPredicate(wrong) == want {
		t.Fatal("wrong homonymous predicate normalized as the expected definition")
	}
}

func TestOptionalPerformanceIndexMigrationFailsOpenAndIsObservable(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	wantErr := errors.New("lock timeout")
	called := false

	migratePerformanceIndexes(nil, func(db *gorm.DB) error {
		called = true
		if db != nil {
			t.Fatal("test expected the supplied database to pass through unchanged")
		}
		return wantErr
	}, logger)

	if !called {
		t.Fatal("optional performance index migration was not attempted")
	}
	logOutput := output.String()
	for _, fragment := range []string{
		"optional performance index migration failed",
		"API remains available",
		"lock timeout",
		"next startup",
	} {
		if !strings.Contains(logOutput, fragment) {
			t.Errorf("migration failure log is missing %q: %s", fragment, logOutput)
		}
	}
}

func TestOptionalPerformanceIndexMigrationContainsPanic(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))

	migratePerformanceIndexes(nil, func(*gorm.DB) error {
		panic("driver panic")
	}, logger)

	logOutput := output.String()
	for _, fragment := range []string{
		"optional performance index migration panicked",
		"API remains available",
		"driver panic",
	} {
		if !strings.Contains(logOutput, fragment) {
			t.Errorf("migration panic log is missing %q: %s", fragment, logOutput)
		}
	}
}
