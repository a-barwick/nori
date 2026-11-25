package memstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	neturl "net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestBackendConformance_Memstore(t *testing.T) {
	b := memstore.New()
	t.Cleanup(func() { _ = b.Close() })
	runBackendConformanceSuite(t, b, "mem")
	runLeaseConformanceSuite(t, b, b, "mem")
	runMemoryIndexConformanceSuite(t, b, b, "mem")
	runRecordsMemoryConformanceSuite(t, b, b, "mem")
}

// isolatedPostgresConformanceURL provisions an ephemeral database (or schema fallback when
// CREATEDB is unavailable) so TestBackendConformance_Postgres never collides with parallel
// state/postgres tests (ClaimTask, ClaimUserWake, global WakeWaitingTasks, QueueMetrics,
// or pg_stat_activity LISTEN assertions).
func isolatedPostgresConformanceURL(t *testing.T, rawURL string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, rawURL)
	if err != nil {
		return rawURL
	}
	defer conn.Close(ctx)

	ident := "nori_pgconf_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	trimmed := strings.TrimSpace(rawURL)
	if strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://") {
		if parsed, err := neturl.Parse(trimmed); err == nil {
			if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident); err == nil {
				t.Cleanup(func() {
					dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer dropCancel()
					c, err := pgx.Connect(dropCtx, rawURL)
					if err != nil {
						t.Logf("cleanup connect to drop database %s: %v", ident, err)
						return
					}
					defer c.Close(dropCtx)
					if _, err := c.Exec(dropCtx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
						t.Logf("cleanup drop database %s: %v", ident, err)
					}
				})
				parsed.Path = "/" + ident
				return parsed.String()
			}
			// Fall back to schema isolation when the test role lacks CREATEDB.
			if _, err := conn.Exec(ctx, "CREATE SCHEMA "+ident); err == nil {
				t.Cleanup(func() {
					dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer dropCancel()
					c, err := pgx.Connect(dropCtx, rawURL)
					if err != nil {
						t.Logf("cleanup connect to drop schema %s: %v", ident, err)
						return
					}
					defer c.Close(dropCtx)
					if _, err := c.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+ident+" CASCADE"); err != nil {
						t.Logf("cleanup drop schema %s: %v", ident, err)
					}
				})
				q := parsed.Query()
				q.Set("search_path", ident)
				parsed.RawQuery = q.Encode()
				return parsed.String()
			}
		}
	}
	return rawURL
}

func TestBackendConformance_Postgres(t *testing.T) {
	rawURL := os.Getenv("NORI_TEST_POSTGRES_URL")
	if rawURL == "" {
		t.Skip("NORI_TEST_POSTGRES_URL not set; skipping Postgres conformance suite")
	}
	ctx := context.Background()
	testURL := isolatedPostgresConformanceURL(t, rawURL)
	b, err := postgres.OpenBackend(ctx, testURL)
	if err != nil {
		t.Fatalf("postgres.OpenBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	prefix := "pgtest_" + uuid.NewString()[:8]
	runBackendConformanceSuite(t, b, prefix)
	ls, ok := any(b).(docstore.LeaseStore)
	if !ok {
		t.Fatalf("postgres backend does not implement docstore.LeaseStore")
	}
	runLeaseConformanceSuite(t, b, ls, prefix)
	idx, ok := docstore.AsMemoryIndex(b)
	if !ok {
		t.Fatalf("postgres backend does not implement docstore.MemoryIndex")
	}
	runMemoryIndexConformanceSuite(t, b, idx, prefix)
	runRecordsMemoryConformanceSuite(t, b, idx, prefix)
	t.Run("Postgres005SchemaPartitionAndBackfill", func(t *testing.T) {
		conn, err := pgx.Connect(ctx, testURL)
		if err != nil {
			t.Fatalf("pgx.Connect: %v", err)
		}
		defer conn.Close(ctx)

		var relkind string
		if err := conn.QueryRow(ctx, `
			SELECT c.relkind::text
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema() AND c.relname = 'nori_memory_chunks'
		`).Scan(&relkind); err != nil {
			t.Fatalf("check nori_memory_chunks relkind: %v", err)
		}
		if relkind != "p" {
			t.Fatalf("nori_memory_chunks relkind = %q, want 'p'", relkind)
		}
		var partCount int
		if err := conn.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_inherits i
			JOIN pg_class parent ON parent.oid = i.inhparent
			JOIN pg_namespace n ON n.oid = parent.relnamespace
			WHERE n.nspname = current_schema() AND parent.relname = 'nori_memory_chunks'
		`).Scan(&partCount); err != nil {
			t.Fatalf("count nori_memory_chunks partitions: %v", err)
		}
		if partCount != 32 {
			t.Fatalf("nori_memory_chunks partitions = %d, want 32", partCount)
		}
		var ginIdx int
		if err := conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes
			WHERE schemaname = current_schema() AND tablename = 'nori_memory_chunks' AND indexname = 'nori_memory_chunks_tsv_gin'
		`).Scan(&ginIdx); err != nil || ginIdx != 1 {
			t.Fatalf("missing nori_memory_chunks_tsv_gin: count=%d err=%v", ginIdx, err)
		}
		var btreeIdx int
		if err := conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes
			WHERE schemaname = current_schema() AND tablename = 'nori_memory_chunks' AND indexname = 'nori_memory_chunks_owner_updated'
		`).Scan(&btreeIdx); err != nil || btreeIdx != 1 {
			t.Fatalf("missing nori_memory_chunks_owner_updated: count=%d err=%v", btreeIdx, err)
		}
		// Zero GIN indexes on nori_records (preserve 004 HOT updates & SSI).
		var recordsGin int
		if err := conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes
			WHERE schemaname = current_schema() AND tablename LIKE 'nori_records%' AND indexdef ILIKE '%USING gin%'
		`).Scan(&recordsGin); err != nil || recordsGin != 0 {
			t.Fatalf("nori_records must have 0 GIN indexes, got %d (err=%v)", recordsGin, err)
		}
	})
}

func runBackendConformanceSuite(t *testing.T, b docstore.Backend, prefix string) {
	t.Helper()
	ctx := context.Background()
	client := docstore.New(b)

	col := func(name string) *docstore.CollectionRef {
		return client.Collection(prefix + "/" + name)
	}

	cleanupPaths := []string{}
	track := func(paths ...string) {
		cleanupPaths = append(cleanupPaths, paths...)
	}
	t.Cleanup(func() {
		if len(cleanupPaths) > 0 {
			_ = b.DeleteBatch(context.Background(), cleanupPaths)
		}
	})

	t.Run("SystemControlSeeded", func(t *testing.T) {
		snap, err := client.Collection("system").Doc("control").Get(ctx)
		if err != nil {
			t.Fatalf("system/control Get: %v", err)
		}
		if _, ok := snap.Data()["paused"].(bool); !ok {
			t.Fatalf("expected boolean paused field in system/control, got %#v", snap.Data())
		}
	})

	t.Run("InvalidPaths", func(t *testing.T) {
		badPaths := []string{"", "noslash", "/leading", "trailing/"}
		for _, p := range badPaths {
			if _, err := b.Get(ctx, p); err == nil {
				t.Errorf("Get(%q): expected error, got nil", p)
			}
			if err := b.Apply(ctx, docstore.Mutation{Kind: "set", Path: p, Fields: map[string]any{"a": 1}}); err == nil {
				t.Errorf("Apply(%q): expected error, got nil", p)
			}
			if err := b.DeleteBatch(ctx, []string{p}); err == nil {
				t.Errorf("DeleteBatch(%q): expected error, got nil", p)
			}
		}
	})

	t.Run("CRUDAndCreateConflict", func(t *testing.T) {
		c := col("crud")
		ref := c.Doc("doc1")
		track(ref.Path)

		if _, err := ref.Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("missing Get: got %v, want ErrNotFound", err)
		}

		type item struct {
			Name    string         `firestore:"name"`
			Count   int            `firestore:"count"`
			Ratio   float64        `firestore:"ratio"`
			When    time.Time      `firestore:"when"`
			Payload []byte         `firestore:"payload"`
			Meta    map[string]any `firestore:"meta"`
		}
		when := time.Date(2026, 5, 1, 12, 30, 45, 123456789, time.UTC)
		orig := item{
			Name:    "alpha",
			Count:   42,
			Ratio:   3.25,
			When:    when,
			Payload: []byte{1, 2, 3, 4},
			Meta:    map[string]any{"tag": "v1"},
		}
		if _, err := ref.Create(ctx, orig); err != nil {
			t.Fatalf("Create: %v", err)
		}
		// Mutate caller map/slice to verify write isolation.
		orig.Meta["tag"] = "mutated"
		orig.Payload[0] = 99

		// Duplicate Create must return docstore.ErrExists.
		if _, err := ref.Create(ctx, map[string]any{"name": "conflict"}); !errors.Is(err, docstore.ErrExists) {
			t.Fatalf("duplicate Create: got %v, want ErrExists", err)
		}

		snap, err := ref.Get(ctx)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got, ok := snap.Data()["count"].(int64); !ok || got != 42 {
			t.Fatalf("NormalizeNumbers int64: got %T(%v), want int64(42)", snap.Data()["count"], snap.Data()["count"])
		}
		if got, ok := snap.Data()["ratio"].(float64); !ok || got != 3.25 {
			t.Fatalf("float64: got %T(%v), want float64(3.25)", snap.Data()["ratio"], snap.Data()["ratio"])
		}
		var decoded item
		if err := snap.DataTo(&decoded); err != nil {
			t.Fatalf("DataTo: %v", err)
		}
		if decoded.Name != "alpha" || decoded.Count != 42 || decoded.Ratio != 3.25 || !decoded.When.Equal(when) {
			t.Fatalf("decoded mismatch: %+v", decoded)
		}
		if !bytes.Equal(decoded.Payload, []byte{1, 2, 3, 4}) {
			t.Fatalf("payload mutated or mismatched: %v", decoded.Payload)
		}
		if decoded.Meta["tag"] != "v1" {
			t.Fatalf("meta mutated or mismatched: %v", decoded.Meta)
		}

		// Mutate returned Data() map and verify subsequent Get is unaffected.
		snap.Data()["name"] = "tampered"
		snap.Data()["meta"].(map[string]any)["tag"] = "tampered"
		snap2, err := ref.Get(ctx)
		if err != nil {
			t.Fatalf("second Get: %v", err)
		}
		if snap2.Data()["name"] != "alpha" || snap2.Data()["meta"].(map[string]any)["tag"] != "v1" {
			t.Fatalf("store leaked mutable reference: %#v", snap2.Data())
		}

		// Set overwrites entire document.
		if _, err := ref.Set(ctx, map[string]any{"only": true}); err != nil {
			t.Fatalf("Set overwrite: %v", err)
		}
		snap3, err := ref.Get(ctx)
		if err != nil {
			t.Fatalf("Get after Set: %v", err)
		}
		if len(snap3.Data()) != 1 || snap3.Data()["only"] != true {
			t.Fatalf("Set did not overwrite cleanly: %#v", snap3.Data())
		}

		// Delete existing and missing.
		if _, err := ref.Delete(ctx); err != nil {
			t.Fatalf("Delete existing: %v", err)
		}
		if _, err := ref.Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("Get after Delete: got %v, want ErrNotFound", err)
		}
		if _, err := ref.Delete(ctx); err != nil {
			t.Fatalf("Delete missing: got %v, want nil", err)
		}
	})

	t.Run("SetMergeAllAndNestedUpdates", func(t *testing.T) {
		c := col("updates")
		ref := c.Doc("u1")
		missing := c.Doc("missing")
		track(ref.Path)

		// Update on missing doc returns ErrNotFound.
		if _, err := missing.Update(ctx, []docstore.Update{{Path: "a", Value: 1}}); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("Update missing: got %v, want ErrNotFound", err)
		}

		// Set with MergeAll creates doc if missing.
		if _, err := ref.Set(ctx, map[string]any{
			"keep": "yes",
			"nested": map[string]any{
				"a": 1,
				"b": 2,
			},
			"tags": []string{"x"},
		}, docstore.MergeAll); err != nil {
			t.Fatalf("Set MergeAll create: %v", err)
		}

		// Set with MergeAll deep-merges nested maps while preserving untouched siblings.
		if _, err := ref.Set(ctx, map[string]any{
			"nested": map[string]any{
				"b": 20,
				"c": 30,
			},
			"added": true,
		}, docstore.MergeAll); err != nil {
			t.Fatalf("Set MergeAll merge: %v", err)
		}

		snap, err := ref.Get(ctx)
		if err != nil {
			t.Fatalf("Get after MergeAll: %v", err)
		}
		nested := snap.Data()["nested"].(map[string]any)
		if snap.Data()["keep"] != "yes" || snap.Data()["added"] != true ||
			nested["a"] != int64(1) || nested["b"] != int64(20) || nested["c"] != int64(30) {
			t.Fatalf("unexpected MergeAll result: %#v", snap.Data())
		}

		// Set with MergeAll empty map replaces existing nested map (matches postgres/database.go).
		if _, err := ref.Set(ctx, map[string]any{
			"nested": map[string]any{},
		}, docstore.MergeAll); err != nil {
			t.Fatalf("Set MergeAll empty map: %v", err)
		}
		snap, err = ref.Get(ctx)
		if err != nil {
			t.Fatalf("Get after empty map merge: %v", err)
		}
		if m, ok := snap.Data()["nested"].(map[string]any); !ok || len(m) != 0 {
			t.Fatalf("expected empty map replacement for nested, got %#v", snap.Data()["nested"])
		}

		// Restore nested map before nested dot-path updates.
		if _, err := ref.Set(ctx, map[string]any{
			"nested": map[string]any{"a": 1, "b": 20, "c": 30},
		}, docstore.MergeAll); err != nil {
			t.Fatalf("restore nested: %v", err)
		}

		// Nested dot-path Update, Delete, and ArrayUnion (primitives + structs).
		type entry struct {
			ID   string `firestore:"id"`
			Text string `firestore:"text"`
		}
		if _, err := ref.Update(ctx, []docstore.Update{
			{Path: "nested.a", Value: docstore.Delete},
			{Path: "deep.new.key", Value: "created"},
			{Path: "keep", Value: docstore.Delete},
			{Path: "tags", Value: docstore.ArrayUnion("x", "y", "y", "z")},
			{Path: "events", Value: docstore.ArrayUnion(entry{ID: "e1", Text: "one"}, entry{ID: "e2", Text: "two"})},
			{Path: "events", Value: docstore.ArrayUnion(entry{ID: "e1", Text: "one"}, entry{ID: "e3", Text: "three"})},
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}

		snap, err = ref.Get(ctx)
		if err != nil {
			t.Fatalf("Get after Update: %v", err)
		}
		if _, exists := snap.Data()["keep"]; exists {
			t.Fatalf("expected 'keep' to be deleted, got %#v", snap.Data())
		}
		nested = snap.Data()["nested"].(map[string]any)
		if _, exists := nested["a"]; exists {
			t.Fatalf("expected 'nested.a' to be deleted, got %#v", nested)
		}
		if snap.Data()["deep"].(map[string]any)["new"].(map[string]any)["key"] != "created" {
			t.Fatalf("expected deep.new.key='created', got %#v", snap.Data()["deep"])
		}
		tags := snap.Data()["tags"].([]any)
		if len(tags) != 3 || tags[0] != "x" || tags[1] != "y" || tags[2] != "z" {
			t.Fatalf("unexpected tags after ArrayUnion: %#v", tags)
		}
		events := snap.Data()["events"].([]any)
		if len(events) != 3 {
			t.Fatalf("expected 3 deduplicated struct events, got %d: %#v", len(events), events)
		}
	})

	t.Run("QueryFiltersOrdersCursorsLimitsSelect", func(t *testing.T) {
		c := col("queries")
		t1 := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
		t2 := time.Date(2026, 1, 1, 0, 0, 1, 500000000, time.UTC)
		t3 := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)

		docs := []struct {
			id   string
			data map[string]any
		}{
			{"d1", map[string]any{"group": "a", "score": 10, "created_at": t1, "active": true, "index": map[string]any{"status": "open", "rank": 1}}},
			{"d2", map[string]any{"group": "a", "score": 20, "created_at": t2, "active": true, "index": map[string]any{"status": "open", "rank": 2}}},
			{"d3", map[string]any{"group": "a", "score": 20, "created_at": t2, "active": false, "index": map[string]any{"status": "closed", "rank": 3}}},
			{"d4", map[string]any{"group": "b", "score": 30, "created_at": t3, "active": true, "opt": nil, "index": map[string]any{"status": "open", "rank": 4}}},
			// d5 intentionally omits score and created_at to test ORDER BY exclusion.
			{"d5", map[string]any{"group": "b", "active": false, "index": map[string]any{"status": "pending", "rank": 5}}},
		}
		for _, d := range docs {
			ref := c.Doc(d.id)
			track(ref.Path)
			if _, err := ref.Set(ctx, d.data); err != nil {
				t.Fatalf("Set %s: %v", d.id, err)
			}
		}

		idsOf := func(t *testing.T, q docstore.Query) []string {
			t.Helper()
			snaps, err := q.Documents(ctx).GetAll()
			if err != nil {
				t.Fatalf("Query failed: %v", err)
			}
			out := make([]string, len(snaps))
			for i, s := range snaps {
				out[i] = s.Ref.ID
			}
			return out
		}

		// Default order is DocumentID Asc.
		if got := idsOf(t, c.Query); !slices.Equal(got, []string{"d1", "d2", "d3", "d4", "d5"}) {
			t.Fatalf("default order: got %v", got)
		}

		// Operators: ==, >, >=, <, <=, in, nested field, DocumentID filter.
		if got := idsOf(t, c.Where("score", "==", 20)); !slices.Equal(got, []string{"d2", "d3"}) {
			t.Fatalf("== 20: got %v", got)
		}
		if got := idsOf(t, c.Where("score", ">", 10).Where("score", "<=", 20)); !slices.Equal(got, []string{"d2", "d3"}) {
			t.Fatalf(">10 <=20: got %v", got)
		}
		if got := idsOf(t, c.Where("score", ">=", 20).Where("score", "<", 30)); !slices.Equal(got, []string{"d2", "d3"}) {
			t.Fatalf(">=20 <30: got %v", got)
		}
		if got := idsOf(t, c.Where("index.status", "in", []string{"closed", "pending"})); !slices.Equal(got, []string{"d3", "d5"}) {
			t.Fatalf("nested in: got %v", got)
		}
		if got := idsOf(t, c.Where("index.status", "in", []string{})); len(got) != 0 {
			t.Fatalf("empty in: got %v, want empty", got)
		}
		if got := idsOf(t, c.Where("created_at", ">", t1)); !slices.Equal(got, []string{"d2", "d3", "d4"}) {
			t.Fatalf("timestamp >: got %v", got)
		}
		if got := idsOf(t, c.Where(docstore.DocumentID, ">=", "d3").Where(docstore.DocumentID, "<", "d5")); !slices.Equal(got, []string{"d3", "d4"}) {
			t.Fatalf("DocumentID range: got %v", got)
		}
		// Absent field vs explicit JSON null.
		if got := idsOf(t, c.Where("opt", "==", nil)); !slices.Equal(got, []string{"d4"}) {
			t.Fatalf("opt == nil: got %v, want [d4]", got)
		}

		// Missing-field exclusion on ORDER BY + trailing DocumentID tiebreaker direction.
		// score Asc -> tiebreaker DocumentID Asc: d1(10), d2(20), d3(20), d4(30); d5 excluded!
		if got := idsOf(t, c.OrderBy("score", docstore.Asc)); !slices.Equal(got, []string{"d1", "d2", "d3", "d4"}) {
			t.Fatalf("OrderBy score Asc: got %v", got)
		}
		// score Desc -> tiebreaker DocumentID Desc: d4(30), d3(20), d2(20), d1(10); d5 excluded!
		if got := idsOf(t, c.OrderBy("score", docstore.Desc)); !slices.Equal(got, []string{"d4", "d3", "d2", "d1"}) {
			t.Fatalf("OrderBy score Desc: got %v", got)
		}

		// StartAfter pagination with created_at Desc (with ties on t2!) and Limit(2).
		page1, err := c.OrderBy("created_at", docstore.Desc).OrderBy(docstore.DocumentID, docstore.Desc).Limit(2).Documents(ctx).GetAll()
		if err != nil {
			t.Fatalf("page1: %v", err)
		}
		if len(page1) != 2 || page1[0].Ref.ID != "d4" || page1[1].Ref.ID != "d3" {
			t.Fatalf("page1 unexpected: %v", page1)
		}
		page2, err := c.OrderBy("created_at", docstore.Desc).OrderBy(docstore.DocumentID, docstore.Desc).StartAfter(page1[1]).Limit(2).Documents(ctx).GetAll()
		if err != nil {
			t.Fatalf("page2: %v", err)
		}
		if len(page2) != 2 || page2[0].Ref.ID != "d2" || page2[1].Ref.ID != "d1" {
			t.Fatalf("page2 unexpected: %v", page2)
		}
		page3, err := c.OrderBy("created_at", docstore.Desc).OrderBy(docstore.DocumentID, docstore.Desc).StartAfter(page2[1]).Limit(2).Documents(ctx).GetAll()
		if err != nil {
			t.Fatalf("page3: %v", err)
		}
		if len(page3) != 0 {
			t.Fatalf("page3 expected empty, got %d", len(page3))
		}

		// StartAfter with synthetic DocumentSnapshot{Ref: c.Doc(id)} (nil Fields), matching records/tasks.go.
		afterRef := &docstore.DocumentSnapshot{Ref: c.Doc("d2")}
		if got := idsOf(t, c.OrderBy(docstore.DocumentID, docstore.Asc).StartAfter(afterRef).Limit(2)); !slices.Equal(got, []string{"d3", "d4"}) {
			t.Fatalf("StartAfter DocumentID synthetic snapshot: got %v", got)
		}

		// Select projection.
		proj, err := c.Where("group", "==", "a").OrderBy(docstore.DocumentID, docstore.Asc).Select("score").Limit(1).Documents(ctx).GetAll()
		if err != nil {
			t.Fatalf("Select query: %v", err)
		}
		if len(proj) != 1 || len(proj[0].Data()) != 1 || proj[0].Data()["score"] != int64(10) {
			t.Fatalf("Select projection unexpected: %#v", proj[0].Data())
		}

		// Invalid field name and unsupported operator return errors.
		if _, err := c.Where("bad-field", "==", 1).Documents(ctx).GetAll(); err == nil {
			t.Fatalf("expected error on invalid field name")
		}
		if _, err := c.Where("score", "!=", 1).Documents(ctx).GetAll(); err == nil {
			t.Fatalf("expected error on unsupported operator")
		}
		if got := idsOf(t, c.Where(docstore.DocumentID, "in", []string{"d2", "d4"})); !slices.Equal(got, []string{"d2", "d4"}) {
			t.Fatalf("DocumentID in: got %v", got)
		}
		if got := idsOf(t, c.Where(docstore.DocumentID, "in", []string{})); len(got) != 0 {
			t.Fatalf("DocumentID empty in: got %v", got)
		}
	})

	t.Run("NestedStartAfterCursorPagination", func(t *testing.T) {
		c := col("nested_cursors")
		seed := []struct {
			id   string
			rank int
		}{
			{"n1", 1},
			{"n2", 2},
			{"n3", 2},
			{"n4", 3},
			{"n5", 4},
		}
		for _, item := range seed {
			ref := c.Doc(item.id)
			track(ref.Path)
			if _, err := ref.Set(ctx, map[string]any{"index": map[string]any{"rank": item.rank}}); err != nil {
				t.Fatalf("Set %s: %v", item.id, err)
			}
		}
		var ascIDs []string
		var cursor *docstore.DocumentSnapshot
		for {
			q := c.OrderBy("index.rank", docstore.Asc).Limit(2)
			if cursor != nil {
				q = q.StartAfter(cursor)
			}
			page, err := q.Documents(ctx).GetAll()
			if err != nil {
				t.Fatalf("nested Asc page: %v", err)
			}
			if len(page) == 0 {
				break
			}
			for _, snap := range page {
				ascIDs = append(ascIDs, snap.Ref.ID)
			}
			cursor = page[len(page)-1]
		}
		if !slices.Equal(ascIDs, []string{"n1", "n2", "n3", "n4", "n5"}) {
			t.Fatalf("nested Asc pagination: got %v", ascIDs)
		}
		var descIDs []string
		cursor = nil
		for {
			q := c.OrderBy("index.rank", docstore.Desc).Limit(2)
			if cursor != nil {
				q = q.StartAfter(cursor)
			}
			page, err := q.Documents(ctx).GetAll()
			if err != nil {
				t.Fatalf("nested Desc page: %v", err)
			}
			if len(page) == 0 {
				break
			}
			for _, snap := range page {
				descIDs = append(descIDs, snap.Ref.ID)
			}
			cursor = page[len(page)-1]
		}
		if !slices.Equal(descIDs, []string{"n5", "n4", "n3", "n2", "n1"}) {
			t.Fatalf("nested Desc pagination: got %v", descIDs)
		}
	})

	t.Run("TransactionPanicDuplicateCreateAndDefaultAttempts", func(t *testing.T) {
		c := col("tx_edge")
		existing := c.Doc("existing")
		panicDoc := c.Doc("panic_doc")
		dupDoc := c.Doc("dup_doc")
		zeroDoc := c.Doc("zero_attempts")
		negDoc := c.Doc("neg_attempts")
		seqDoc := col("tx_edge_seq").Doc("head")
		track(existing.Path, panicDoc.Path, dupDoc.Path, zeroDoc.Path, negDoc.Path)

		if _, err := existing.Create(ctx, map[string]any{"v": 1}); err != nil {
			t.Fatalf("Create existing: %v", err)
		}
		if err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			_, _, err := tx.NextSequence(seqDoc, 1)
			return err
		}); err != nil {
			t.Fatalf("seed seq: %v", err)
		}

		var recovered any
		func() {
			defer func() {
				recovered = recover()
			}()
			_ = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
				if err := tx.Create(panicDoc, map[string]any{"v": 1}); err != nil {
					return err
				}
				if err := tx.Update(existing, []docstore.Update{{Path: "v", Value: 99}}); err != nil {
					return err
				}
				if _, _, err := tx.NextSequence(seqDoc, 5); err != nil {
					return err
				}
				panic("simulated tx panic")
			})
		}()
		if recovered != "simulated tx panic" {
			t.Fatalf("expected panic to propagate, got %v", recovered)
		}
		if _, err := panicDoc.Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("panicDoc should have rolled back, got %v", err)
		}
		snap, err := existing.Get(ctx)
		if err != nil || snap.Data()["v"] != int64(1) {
			t.Fatalf("existing should remain v=1 after panic rollback, got %#v err=%v", snap, err)
		}
		if head, _, err := client.CurrentSequence(ctx, seqDoc); err != nil || head != 1 {
			t.Fatalf("sequence should remain 1 after panic rollback, got %d err=%v", head, err)
		}
		if err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			return tx.Update(existing, []docstore.Update{{Path: "v", Value: 2}})
		}); err != nil {
			t.Fatalf("post-panic tx failed: %v", err)
		}
		snap, err = existing.Get(ctx)
		if err != nil || snap.Data()["v"] != int64(2) {
			t.Fatalf("existing should be v=2 after post-panic tx, got %#v err=%v", snap, err)
		}

		if _, err := dupDoc.Create(ctx, map[string]any{"v": 1}); err != nil {
			t.Fatalf("Create dupDoc: %v", err)
		}
		dupCalls := 0
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			dupCalls++
			return tx.Create(dupDoc, map[string]any{"v": 2})
		}, docstore.MaxAttempts(5))
		if !errors.Is(err, docstore.ErrExists) {
			t.Fatalf("duplicate Create in tx: got %v, want ErrExists", err)
		}
		if dupCalls != 1 {
			t.Fatalf("duplicate Create retried %d times, want 1", dupCalls)
		}

		zeroCalls := 0
		err = b.RunTransaction(ctx, false, 0, func(tx docstore.TransactionBackend) error {
			zeroCalls++
			return tx.Apply(ctx, docstore.Mutation{Kind: "create", Path: zeroDoc.Path, Fields: map[string]any{"v": 10}})
		})
		if err != nil || zeroCalls != 1 {
			t.Fatalf("attempts=0: err=%v calls=%d", err, zeroCalls)
		}
		if snap, err := zeroDoc.Get(ctx); err != nil || snap.Data()["v"] != int64(10) {
			t.Fatalf("zeroDoc missing after attempts=0 tx: snap=%v err=%v", snap, err)
		}

		negCalls := 0
		err = b.RunTransaction(ctx, false, -3, func(tx docstore.TransactionBackend) error {
			negCalls++
			return tx.Apply(ctx, docstore.Mutation{Kind: "create", Path: negDoc.Path, Fields: map[string]any{"v": 20}})
		})
		if err != nil || negCalls != 1 {
			t.Fatalf("attempts=-3: err=%v calls=%d", err, negCalls)
		}
		if snap, err := negDoc.Get(ctx); err != nil || snap.Data()["v"] != int64(20) {
			t.Fatalf("negDoc missing after attempts=-3 tx: snap=%v err=%v", snap, err)
		}
	})

	t.Run("TransactionsRollbackAndSequencer", func(t *testing.T) {
		c := col("tx")
		d1 := c.Doc("t1")
		d2 := c.Doc("t2")
		seqDoc := col("seq").Doc("head")
		track(d1.Path, d2.Path)

		// Read-your-own-writes + commit + Sequencer allocation.
		var seqStart1, seqStart2 int64
		err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if !tx.HasSequencer() {
				t.Fatalf("expected backend transaction to implement Sequencer")
			}
			if err := tx.Create(d1, map[string]any{"v": 1}); err != nil {
				return err
			}
			snap, err := tx.Get(d1)
			if err != nil {
				return err
			}
			if snap.Data()["v"] != int64(1) {
				t.Fatalf("tx did not see own write: %#v", snap.Data())
			}
			rows, err := tx.Documents(c.Where("v", "==", 1)).GetAll()
			if err != nil || len(rows) != 1 || rows[0].Ref.ID != "t1" {
				t.Fatalf("tx Query did not see own write: rows=%d err=%v", len(rows), err)
			}
			s1, ok, err := tx.NextSequence(seqDoc, 2)
			if err != nil || !ok {
				t.Fatalf("NextSequence #1: ok=%v err=%v", ok, err)
			}
			s2, ok, err := tx.NextSequence(seqDoc, 3)
			if err != nil || !ok {
				t.Fatalf("NextSequence #2: ok=%v err=%v", ok, err)
			}
			seqStart1, seqStart2 = s1, s2
			return nil
		})
		if err != nil {
			t.Fatalf("RunTransaction commit: %v", err)
		}
		if seqStart1 != 1 || seqStart2 != 3 {
			t.Fatalf("unexpected sequence starts: %d, %d (want 1, 3)", seqStart1, seqStart2)
		}
		head, ok, err := client.CurrentSequence(ctx, seqDoc)
		if err != nil || !ok || head != 5 {
			t.Fatalf("CurrentSequence after commit: head=%d ok=%v err=%v (want 5)", head, ok, err)
		}

		// Rollback on callback error must roll back BOTH doc writes AND NextSequence!
		sentinel := errors.New("abort tx")
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.Create(d2, map[string]any{"v": 2}); err != nil {
				return err
			}
			if err := tx.Update(d1, []docstore.Update{{Path: "v", Value: 999}}); err != nil {
				return err
			}
			if _, _, err := tx.NextSequence(seqDoc, 10); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("aborted tx err: got %v, want %v", err, sentinel)
		}
		if _, err := d2.Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("d2 should have rolled back, got %v", err)
		}
		snap1, err := d1.Get(ctx)
		if err != nil || snap1.Data()["v"] != int64(1) {
			t.Fatalf("d1 should remain v=1 after rollback, got %#v err=%v", snap1, err)
		}
		headAfterAbort, _, err := client.CurrentSequence(ctx, seqDoc)
		if err != nil || headAfterAbort != 5 {
			t.Fatalf("sequence should remain 5 after tx abort, got %d err=%v", headAfterAbort, err)
		}

		// Next committed sequence should start at 6 (no gap from aborted tx).
		var seqAfter int64
		if err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			s, _, err := tx.NextSequence(seqDoc, 1)
			seqAfter = s
			return err
		}); err != nil {
			t.Fatalf("post-abort NextSequence tx: %v", err)
		}
		if seqAfter != 6 {
			t.Fatalf("expected next seq 6 after abort, got %d", seqAfter)
		}

		// Read-only transaction rejects writes.
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			return tx.Set(d2, map[string]any{"ro": true})
		}, docstore.ReadOnly)
		if err == nil {
			t.Fatalf("expected error writing inside ReadOnly transaction")
		}
	})

	t.Run("DeleteBatch", func(t *testing.T) {
		c1 := col("del1")
		c2 := col("del2")
		p1 := c1.Doc("a").Path
		p2 := c2.Doc("b").Path
		pMissing := c2.Doc("nope").Path
		track(p1, p2)

		if _, err := c1.Doc("a").Set(ctx, map[string]any{"x": 1}); err != nil {
			t.Fatalf("Set a: %v", err)
		}
		if _, err := c2.Doc("b").Set(ctx, map[string]any{"x": 2}); err != nil {
			t.Fatalf("Set b: %v", err)
		}

		// Invalid path in batch must reject without deleting valid paths.
		if err := b.DeleteBatch(ctx, []string{p1, "invalid"}); err == nil {
			t.Fatalf("expected error for invalid path in DeleteBatch")
		}
		if _, err := c1.Doc("a").Get(ctx); err != nil {
			t.Fatalf("p1 should not have been deleted on invalid batch: %v", err)
		}

		if err := b.DeleteBatch(ctx, []string{p1, p2, pMissing}); err != nil {
			t.Fatalf("DeleteBatch: %v", err)
		}
		if _, err := c1.Doc("a").Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("p1 still exists: %v", err)
		}
		if _, err := c2.Doc("b").Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("p2 still exists: %v", err)
		}
	})

	t.Run("WatchIncrementalEvents", func(t *testing.T) {
		c := col("watch_events")
		for i := 1; i <= 2; i++ {
			ref := c.Doc(fmt.Sprintf("%020d", i))
			track(ref.Path)
			if _, err := ref.Create(ctx, map[string]any{"seq": int64(i), "kind": "init"}); err != nil {
				t.Fatalf("seed event %d: %v", i, err)
			}
		}

		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		var mu sync.Mutex
		seen := []int64{}
		gotTwo := make(chan struct{}, 1)
		gotFour := make(chan struct{}, 1)

		errCh := make(chan error, 1)
		go func() {
			errCh <- c.Where("seq", ">", int64(0)).OrderBy("seq", docstore.Asc).Watch(watchCtx, func(docs []*docstore.DocumentSnapshot) error {
				mu.Lock()
				for _, d := range docs {
					seen = append(seen, d.Data()["seq"].(int64))
				}
				n := len(seen)
				mu.Unlock()
				if n >= 2 {
					select {
					case gotTwo <- struct{}{}:
					default:
					}
				}
				if n >= 4 {
					select {
					case gotFour <- struct{}{}:
					default:
					}
				}
				return nil
			})
		}()

		select {
		case <-gotTwo:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for initial watch batch")
		}

		for i := 3; i <= 4; i++ {
			ref := c.Doc(fmt.Sprintf("%020d", i))
			track(ref.Path)
			if _, err := ref.Create(ctx, map[string]any{"seq": int64(i), "kind": "live"}); err != nil {
				t.Fatalf("live event %d: %v", i, err)
			}
		}

		select {
		case <-gotFour:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for live watch batch")
		}
		cancel()

		if err := <-errCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("Watch exit err: got %v, want context.Canceled", err)
		}
		mu.Lock()
		got := slices.Clone(seen)
		mu.Unlock()
		if !slices.Equal(got, []int64{1, 2, 3, 4}) {
			t.Fatalf("Watch delivered seqs %v, want [1 2 3 4]", got)
		}
	})

	t.Run("RecordsIntegrationSmoke", func(t *testing.T) {
		userID := uuid.NewString()
		user := users.User{
			ID:       userID,
			Email:    "sim-" + userID[:8] + "@example.com",
			TimeZone: "UTC",
		}
		art := memstore.NewArtifacts()
		store := state.Compose(records.NewShared(b, user, 1.0), art)
		defer store.Close()
		t.Cleanup(func() {
			if ls, ok := any(b).(docstore.LeaseStore); ok {
				for i := 0; i < 8; i++ {
					uw, claimed, err := ls.ClaimUserWake(context.Background(), docstore.LaneConversation, "smoke-cleanup", 5*time.Second)
					if err != nil || !claimed {
						break
					}
					if uw.UserID == userID {
						_, _ = ls.ParkUserWake(context.Background(), userID, docstore.LaneConversation, "smoke-cleanup", uw.WakeSeq)
						break
					}
					_ = ls.ReleaseUserWake(context.Background(), uw.UserID, docstore.LaneConversation, "smoke-cleanup", uw.WakeSeq, time.Time{})
				}
			}
		})

		msgID := uuid.NewString()
		if err := store.AcceptConversationInput(ctx, msgID, "hello simulation", nil); err != nil {
			t.Fatalf("AcceptConversationInput: %v", err)
		}
		pos, err := store.EventPosition(ctx)
		if err != nil {
			t.Fatalf("EventPosition: %v", err)
		}
		if pos <= 0 {
			t.Fatalf("EventPosition: got %d, want > 0 after AcceptConversationInput", pos)
		}
		sess, err := store.Session(ctx)
		if err != nil {
			t.Fatalf("Session: %v", err)
		}
		if len(sess.Messages) != 1 || sess.Messages[0].Text != "hello simulation" {
			t.Fatalf("Session messages mismatch: %+v", sess.Messages)
		}
		wv, err := store.WorkVersion(ctx, true)
		if err != nil || wv == "" {
			t.Fatalf("WorkVersion: got %q err=%v", wv, err)
		}
	})
}

func TestMemstoreConcurrentSerializableRetry(t *testing.T) {
	ctx := context.Background()
	b := memstore.New()
	client := docstore.New(b)

	counter := client.Collection("counters").Doc("c1")
	if _, err := counter.Create(ctx, map[string]any{"n": int64(0)}); err != nil {
		t.Fatalf("Create counter: %v", err)
	}
	seqRef := client.Collection("seq").Doc("head")

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	seqCh := make(chan int64, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var allocated int64
			err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
				snap, err := tx.Get(counter)
				if err != nil {
					return err
				}
				n := snap.Data()["n"].(int64)
				if err := tx.Update(counter, []docstore.Update{{Path: "n", Value: n + 1}}); err != nil {
					return err
				}
				s, _, err := tx.NextSequence(seqRef, 1)
				if err != nil {
					return err
				}
				allocated = s
				return nil
			}, docstore.MaxAttempts(workers*4))
			if err != nil {
				errs <- err
				return
			}
			seqCh <- allocated
		}()
	}
	wg.Wait()
	close(errs)
	close(seqCh)

	for err := range errs {
		t.Fatalf("concurrent tx failed: %v", err)
	}
	snap, err := counter.Get(ctx)
	if err != nil {
		t.Fatalf("Get counter: %v", err)
	}
	if got := snap.Data()["n"].(int64); got != workers {
		t.Fatalf("counter lost updates: got %d, want %d", got, workers)
	}
	seqs := make([]int64, 0, workers)
	for s := range seqCh {
		seqs = append(seqs, s)
	}
	slices.Sort(seqs)
	for i := 0; i < workers; i++ {
		if seqs[i] != int64(i+1) {
			t.Fatalf("concurrent sequences not strictly 1..%d: %v", workers, seqs)
		}
	}
}

func TestArtifactsAndComposeStore(t *testing.T) {
	ctx := context.Background()
	b := memstore.New()
	a := memstore.NewArtifacts()

	runID := uuid.NewString()
	artID := uuid.NewString()

	// PutPNG / GetPNG + deep copy isolation + duplicate refusal.
	pngBytes := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	if err := a.PutPNG(ctx, runID, artID, pngBytes); err != nil {
		t.Fatalf("PutPNG: %v", err)
	}
	pngBytes[0] = 0x00 // mutate caller buffer
	if err := a.PutPNG(ctx, runID, artID, []byte{9}); !errors.Is(err, state.ErrFileExists) {
		t.Fatalf("duplicate PutPNG: got %v, want ErrFileExists", err)
	}
	gotPNG, err := a.GetPNG(ctx, runID, artID)
	if err != nil {
		t.Fatalf("GetPNG: %v", err)
	}
	if !bytes.Equal(gotPNG, []byte{0x89, 'P', 'N', 'G', 1, 2, 3}) {
		t.Fatalf("GetPNG mismatch: %v", gotPNG)
	}
	gotPNG[0] = 0xff // mutate returned buffer
	gotPNG2, err := a.GetPNG(ctx, runID, artID)
	if err != nil || gotPNG2[0] != 0x89 {
		t.Fatalf("GetPNG leaked mutable slice: %v err=%v", gotPNG2, err)
	}
	if _, err := a.GetPNG(ctx, runID, uuid.NewString()); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("missing GetPNG: got %v, want ErrNotFound", err)
	}

	// PutFile / GetFile / DeleteFile.
	fileKey := "uploads/" + uuid.NewString() + ".bin"
	fileBytes := []byte("hello-file")
	if err := a.PutFile(ctx, fileKey, "application/octet-stream", fileBytes); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	fileBytes[0] = 'X'
	if err := a.PutFile(ctx, fileKey, "application/octet-stream", []byte("other")); !errors.Is(err, state.ErrFileExists) {
		t.Fatalf("duplicate PutFile: got %v, want ErrFileExists", err)
	}
	gotFile, err := a.GetFile(ctx, fileKey)
	if err != nil || !bytes.Equal(gotFile, []byte("hello-file")) {
		t.Fatalf("GetFile: got %q err=%v", gotFile, err)
	}
	gotFile[0] = 'Y'
	gotFile2, err := a.GetFile(ctx, fileKey)
	if err != nil || !bytes.Equal(gotFile2, []byte("hello-file")) {
		t.Fatalf("GetFile leaked mutable slice: %q err=%v", gotFile2, err)
	}
	if err := a.DeleteFile(ctx, fileKey); err != nil {
		t.Fatalf("DeleteFile existing: %v", err)
	}
	if err := a.DeleteFile(ctx, fileKey); err != nil {
		t.Fatalf("DeleteFile missing: got %v, want nil", err)
	}
	if _, err := a.GetFile(ctx, fileKey); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("GetFile after delete: got %v, want ErrNotFound", err)
	}

	// ComposeStore enforces state.Compose rules (ValidFileKey, ArtifactListed).
	user := users.User{
		ID:       uuid.NewString(),
		Email:    "compose@example.com",
		TimeZone: "UTC",
	}
	store := memstore.ComposeStore(b, a, user, 2.0)
	defer store.Close()

	if err := store.PutFile(ctx, "../escape", "text/plain", []byte("no")); err == nil {
		t.Fatalf("ComposeStore should reject invalid file key")
	}
	validKey := state.UploadKey(uuid.NewString())
	if err := store.PutFile(ctx, validKey, "text/plain", []byte("ok")); err != nil {
		t.Fatalf("ComposeStore PutFile: %v", err)
	}
	got, err := store.GetFile(ctx, validKey)
	if err != nil || string(got) != "ok" {
		t.Fatalf("ComposeStore GetFile: got %q err=%v", got, err)
	}
	if err := store.AcceptConversationInput(ctx, uuid.NewString(), "composed input", nil); err != nil {
		t.Fatalf("ComposeStore AcceptConversationInput: %v", err)
	}
	sess, err := store.Session(ctx)
	if err != nil || len(sess.Messages) != 1 || sess.Messages[0].Text != "composed input" {
		t.Fatalf("ComposeStore Session: sess=%+v err=%v", sess, err)
	}
}

func TestFaultInjection_TransientConflictsAndCommitThenError(t *testing.T) {
	ctx := context.Background()

	t.Run("TransientGetQueryApplyAndDisableToggle", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(10, 20))
		b := memstore.New(memstore.WithFaults(memstore.FaultConfig{
			GetErrProb:   1.0,
			QueryErrProb: 1.0,
			ApplyErrProb: 1.0,
		}, rng))
		client := docstore.New(b)

		if _, err := client.Collection("items").Doc("i1").Create(ctx, map[string]any{"x": 1}); !errors.Is(err, memstore.ErrInjectedFault) {
			t.Fatalf("expected ErrInjectedFault on Apply, got %v", err)
		}
		if _, err := client.Collection("items").Doc("i1").Get(ctx); !errors.Is(err, memstore.ErrInjectedFault) {
			t.Fatalf("expected ErrInjectedFault on Get, got %v", err)
		}
		if _, err := client.Collection("items").Documents(ctx).GetAll(); !errors.Is(err, memstore.ErrInjectedFault) {
			t.Fatalf("expected ErrInjectedFault on Query, got %v", err)
		}

		stats := b.FaultStats()
		if stats.ApplyErrors != 1 || stats.GetErrors != 1 || stats.QueryErrors != 1 {
			t.Fatalf("unexpected fault stats: %+v", stats)
		}

		// Disable faults for quiescence drain: all operations succeed and stats stay frozen.
		b.SetFaultsEnabled(false)
		if _, err := client.Collection("items").Doc("i1").Create(ctx, map[string]any{"x": 1}); err != nil {
			t.Fatalf("Create with faults disabled: %v", err)
		}
		if _, err := client.Collection("items").Doc("i1").Get(ctx); err != nil {
			t.Fatalf("Get with faults disabled: %v", err)
		}
		if rows, err := client.Collection("items").Documents(ctx).GetAll(); err != nil || len(rows) != 1 {
			t.Fatalf("Query with faults disabled: rows=%d err=%v", len(rows), err)
		}
		if after := b.FaultStats(); after != stats {
			t.Fatalf("stats changed while faults disabled: %+v vs %+v", after, stats)
		}
	})

	t.Run("TxConflictRetryAndExhaustion", func(t *testing.T) {
		// Deterministic sequence: fail first 2 commit attempts, succeed on 3rd.
		seq := &scriptedFloats{vals: []float64{0.1, 0.1, 0.9}}
		b := memstore.New(memstore.WithFaults(memstore.FaultConfig{TxConflictProb: 0.5}, seq))
		client := docstore.New(b)

		var runs int
		err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			runs++
			return tx.Set(client.Collection("c").Doc("d"), map[string]any{"attempt": runs})
		}, docstore.MaxAttempts(5))
		if err != nil {
			t.Fatalf("expected tx to succeed on 3rd attempt, got %v", err)
		}
		if runs != 3 {
			t.Fatalf("expected 3 attempts, got %d", runs)
		}
		stats := b.FaultStats()
		if stats.TxConflicts != 2 || stats.TxConflictsExhausted != 0 {
			t.Fatalf("unexpected conflict stats: %+v", stats)
		}

		// Now force exhaustion (100% conflict over 3 attempts).
		b.SetFaults(memstore.FaultConfig{TxConflictProb: 1.0}, rand.New(rand.NewPCG(7, 8)))
		runs = 0
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			runs++
			return tx.Set(client.Collection("c").Doc("exhaust"), map[string]any{"v": 1})
		}, docstore.MaxAttempts(3))
		if !errors.Is(err, memstore.ErrTxConflict) {
			t.Fatalf("expected ErrTxConflict on exhaustion, got %v", err)
		}
		if runs != 3 {
			t.Fatalf("expected 3 exhausted attempts, got %d", runs)
		}
		stats = b.FaultStats()
		if stats.TxConflicts != 5 || stats.TxConflictsExhausted != 1 {
			t.Fatalf("unexpected exhaustion stats: %+v", stats)
		}
		b.SetFaultsEnabled(false)
		if _, err := client.Collection("c").Doc("exhaust").Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("exhausted tx must not have committed: %v", err)
		}
	})

	t.Run("CommitThenErrorOnTransactionApplyDeleteBatchAndArtifacts", func(t *testing.T) {
		inj := memstore.NewFaultInjector(memstore.FaultConfig{
			CommitThenErrProb: 1.0,
		}, rand.New(rand.NewPCG(42, 99)))
		b := memstore.New(memstore.WithFaultInjector(inj))
		a := memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(inj))
		client := docstore.New(b)

		// 1. RunTransaction commit-then-error: error returned, BUT doc AND seq committed durably!
		seqRef := client.Collection("seq").Doc("head")
		err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.Create(client.Collection("c").Doc("txdoc"), map[string]any{"committed": true}); err != nil {
				return err
			}
			_, _, err := tx.NextSequence(seqRef, 2)
			return err
		})
		if !errors.Is(err, memstore.ErrCommitThenError) || !errors.Is(err, memstore.ErrInjectedFault) {
			t.Fatalf("RunTransaction commit-then-error: got %v", err)
		}
		snap, err := client.Collection("c").Doc("txdoc").Get(ctx)
		if err != nil || snap.Data()["committed"] != true {
			t.Fatalf("txdoc should be durably committed despite error: snap=%v err=%v", snap, err)
		}
		if seq, _, err := client.CurrentSequence(ctx, seqRef); err != nil || seq != 2 {
			t.Fatalf("sequence should be durably 2 despite error: seq=%d err=%v", seq, err)
		}

		// 2. Apply commit-then-error.
		if _, err := client.Collection("c").Doc("applydoc").Set(ctx, map[string]any{"k": "v"}); !errors.Is(err, memstore.ErrCommitThenError) {
			t.Fatalf("Apply commit-then-error: got %v", err)
		}
		if snap, err := client.Collection("c").Doc("applydoc").Get(ctx); err != nil || snap.Data()["k"] != "v" {
			t.Fatalf("applydoc should be durably committed: snap=%v err=%v", snap, err)
		}

		// 3. DeleteBatch commit-then-error.
		if err := b.DeleteBatch(ctx, []string{"c/applydoc"}); !errors.Is(err, memstore.ErrCommitThenError) {
			t.Fatalf("DeleteBatch commit-then-error: got %v", err)
		}
		if _, err := client.Collection("c").Doc("applydoc").Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("applydoc should be durably deleted: err=%v", err)
		}

		// 4. PutFile commit-then-error: error returned, BUT file stored durably!
		if err := a.PutFile(ctx, "uploads/f1.txt", "text/plain", []byte("durable")); !errors.Is(err, memstore.ErrCommitThenError) {
			t.Fatalf("PutFile commit-then-error: got %v", err)
		}
		data, err := a.GetFile(ctx, "uploads/f1.txt")
		if err != nil || string(data) != "durable" {
			t.Fatalf("file should be durably stored despite error: data=%q err=%v", data, err)
		}

		// 5. PutPNG commit-then-error.
		if err := a.PutPNG(ctx, "r1", "a1", []byte("pngdata")); !errors.Is(err, memstore.ErrCommitThenError) {
			t.Fatalf("PutPNG commit-then-error: got %v", err)
		}
		png, err := a.GetPNG(ctx, "r1", "a1")
		if err != nil || string(png) != "pngdata" {
			t.Fatalf("PNG should be durably stored despite error: data=%q err=%v", png, err)
		}

		// Shared FaultInjector tracked all 5 commit-then-error events.
		if stats := inj.Stats(); stats.CommitThenErrors != 5 {
			t.Fatalf("expected 5 CommitThenErrors, got %+v", stats)
		}

		// Disabling shared FaultInjector disables faults on both Backend and Artifacts.
		inj.SetEnabled(false)
		if err := a.PutFile(ctx, "uploads/f2.txt", "text/plain", []byte("clean")); err != nil {
			t.Fatalf("PutFile with disabled injector: %v", err)
		}
	})

	t.Run("DeterministicSeededFaultReplay", func(t *testing.T) {
		runWorkload := func(seed1, seed2 uint64) (string, memstore.FaultStats, int) {
			rng := rand.New(rand.NewPCG(seed1, seed2))
			inj := memstore.NewFaultInjector(memstore.FaultConfig{
				GetErrProb:        0.2,
				QueryErrProb:      0.2,
				ApplyErrProb:      0.2,
				ArtifactErrProb:   0.2,
				TxConflictProb:    0.3,
				CommitThenErrProb: 0.2,
			}, rng)
			b := memstore.New(memstore.WithFaultInjector(inj))
			a := memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(inj))
			client := docstore.New(b)

			var errCount int
			for i := 0; i < 40; i++ {
				id := fmt.Sprintf("d%02d", i%10)
				if _, err := client.Collection("w").Doc(id).Set(ctx, map[string]any{"i": i, "mod": i % 3}); err != nil {
					errCount++
				}
				if _, err := client.Collection("w").Doc(id).Get(ctx); err != nil {
					errCount++
				}
				if _, err := client.Collection("w").Where("mod", "==", i%3).Documents(ctx).GetAll(); err != nil {
					errCount++
				}
				if err := a.PutFile(ctx, fmt.Sprintf("uploads/f%02d.bin", i), "application/octet-stream", []byte{byte(i)}); err != nil {
					errCount++
				}
			}
			return memstore.CombinedDigest(b, a), inj.Stats(), errCount
		}

		d1, s1, e1 := runWorkload(12345, 67890)
		d2, s2, e2 := runWorkload(12345, 67890)
		if d1 != d2 || s1 != s2 || e1 != e2 {
			t.Fatalf("same seed diverged:\nrun1: digest=%s stats=%+v errs=%d\nrun2: digest=%s stats=%+v errs=%d", d1, s1, e1, d2, s2, e2)
		}
		if s1.Total() == 0 {
			t.Fatalf("expected non-zero injected faults, got %+v", s1)
		}
	})
}

func TestSnapshotAndDigestDeterminism(t *testing.T) {
	ctx := context.Background()

	// Two backends populated in reverse collection, document, and map key order.
	b1 := memstore.New()
	b2 := memstore.New()
	c1 := docstore.New(b1)
	c2 := docstore.New(b2)

	baseDigest := b1.Digest()
	if baseDigest != b2.Digest() {
		t.Fatalf("fresh backends have different digests: %s vs %s", baseDigest, b2.Digest())
	}

	// Creating then deleting a doc must return to exact baseDigest (no empty collection residue).
	if _, err := c1.Collection("temp").Doc("ephemeral").Create(ctx, map[string]any{"x": 1}); err != nil {
		t.Fatalf("Create ephemeral: %v", err)
	}
	if b1.Digest() == baseDigest {
		t.Fatalf("digest did not change after Create")
	}
	if _, err := c1.Collection("temp").Doc("ephemeral").Delete(ctx); err != nil {
		t.Fatalf("Delete ephemeral: %v", err)
	}
	if b1.Digest() != baseDigest {
		t.Fatalf("digest after Create+Delete differs from base: %s vs %s", b1.Digest(), baseDigest)
	}

	// Populate b1 in order A -> B, doc 1 -> 2, keys a -> b; populate b2 in reverse order.
	if _, err := c1.Collection("colA").Doc("1").Set(ctx, map[string]any{"a": 1, "b": map[string]any{"x": true, "y": "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.Collection("colB").Doc("2").Set(ctx, map[string]any{"k": []any{1, 2.5, nil}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b1.NextSequence(ctx, "owners/u1/event_head/head", 3); err != nil {
		t.Fatal(err)
	}

	if _, err := b2.NextSequence(ctx, "owners/u1/event_head/head", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := b2.NextSequence(ctx, "owners/u1/event_head/head", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Collection("colB").Doc("2").Set(ctx, map[string]any{"k": []any{1, 2.5, nil}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Collection("colA").Doc("1").Set(ctx, map[string]any{"b": map[string]any{"y": "hi", "x": true}, "a": 1}); err != nil {
		t.Fatal(err)
	}

	if b1.Digest() != b2.Digest() {
		t.Fatalf("canonical Backend digests diverged: %s vs %s", b1.Digest(), b2.Digest())
	}

	// Mutating Snapshot must not affect live Backend or its Digest.
	snap := b1.Snapshot()
	snapDigest := snap.Digest()
	if snapDigest != b1.Digest() {
		t.Fatalf("Snapshot.Digest != Backend.Digest")
	}
	snap.Collections["colA"]["1"]["a"] = int64(999)
	snap.Sequences["owners/u1/event_head/head"] = 999
	if b1.Digest() != snapDigest {
		t.Fatalf("mutating Snapshot mutated live Backend")
	}

	// Artifacts canonical digest across different insertion orders.
	a1 := memstore.NewArtifacts()
	a2 := memstore.NewArtifacts()
	_ = a1.PutPNG(ctx, "r1", "a1", []byte("one"))
	_ = a1.PutPNG(ctx, "r2", "a2", []byte("two"))
	_ = a1.PutFile(ctx, "f/a", "text/plain", []byte("alpha"))
	_ = a1.PutFile(ctx, "f/b", "image/png", []byte("beta"))

	_ = a2.PutFile(ctx, "f/b", "image/png", []byte("beta"))
	_ = a2.PutFile(ctx, "f/a", "text/plain", []byte("alpha"))
	_ = a2.PutPNG(ctx, "r2", "a2", []byte("two"))
	_ = a2.PutPNG(ctx, "r1", "a1", []byte("one"))

	if a1.Digest() != a2.Digest() {
		t.Fatalf("canonical Artifacts digests diverged: %s vs %s", a1.Digest(), a2.Digest())
	}
	if memstore.CombinedDigest(b1, a1) != memstore.CombinedDigest(b2, a2) {
		t.Fatalf("CombinedDigest diverged")
	}

	// Changing media type alone must change Artifacts digest.
	a3 := memstore.NewArtifacts()
	_ = a3.PutFile(ctx, "f/a", "text/html", []byte("alpha"))
	_ = a3.PutFile(ctx, "f/b", "image/png", []byte("beta"))
	_ = a3.PutPNG(ctx, "r1", "a1", []byte("one"))
	_ = a3.PutPNG(ctx, "r2", "a2", []byte("two"))
	if a1.Digest() == a3.Digest() {
		t.Fatalf("expected different mediaType to produce different digest")
	}
}

type noCloseBackend struct{ docstore.Backend }

func (noCloseBackend) Close() error { return nil }

type noCloseSeqBackend struct {
	docstore.Backend
	docstore.SequenceReader
}

func (noCloseSeqBackend) Close() error { return nil }

type scriptedFloats struct {
	vals []float64
	idx  atomic.Int64
}

func (s *scriptedFloats) Float64() float64 {
	i := int(s.idx.Add(1) - 1)
	if i < len(s.vals) {
		return s.vals[i]
	}
	return 0.99
}

func TestJSONBOrderingAndContextCancellation(t *testing.T) {
	ctx := context.Background()
	b := memstore.New()
	client := docstore.New(b)
	c := client.Collection("jsonb")

	// PostgreSQL JSONB total order: Null < String < Number < Boolean < Array < Object.
	items := []struct {
		id  string
		val any
	}{
		{"n0", nil},
		{"s1", "abc"},
		{"num_int", int64(5)},
		{"num_flt", float64(5.5)},
		{"num_big1", int64(1 << 53)},
		{"num_big2", int64(1<<53 + 1)},
		{"b_false", false},
		{"b_true", true},
		{"arr1", []any{1}},
		{"arr2", []any{1, 2}},
		{"obj1", map[string]any{"a": 1}},
		{"obj2", map[string]any{"a": 1, "b": 2}},
	}
	for _, it := range items {
		if _, err := c.Doc(it.id).Set(ctx, map[string]any{"v": it.val}); err != nil {
			t.Fatalf("Set %s: %v", it.id, err)
		}
	}

	snaps, err := c.OrderBy("v", docstore.Asc).Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("OrderBy v Asc: %v", err)
	}
	got := make([]string, len(snaps))
	for i, s := range snaps {
		got[i] = s.Ref.ID
	}
	want := []string{"n0", "s1", "num_int", "num_flt", "num_big1", "num_big2", "b_false", "b_true", "arr1", "arr2", "obj1", "obj2"}
	if !slices.Equal(got, want) {
		t.Fatalf("JSONB order mismatch:\n got: %v\nwant: %v", got, want)
	}

	// int64(5) == float64(5.0)
	eqSnaps, err := c.Where("v", "==", float64(5.0)).Documents(ctx).GetAll()
	if err != nil || len(eqSnaps) != 1 || eqSnaps[0].Ref.ID != "num_int" {
		t.Fatalf("float64(5.0) == int64(5): got %v err=%v", eqSnaps, err)
	}

	// Context cancellation respected across Backend and Artifacts.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	a := memstore.NewArtifacts()
	if _, err := b.Get(canceled, "jsonb/n0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get canceled: %v", err)
	}
	if _, err := b.Query(canceled, docstore.QuerySpec{Collection: "jsonb"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Query canceled: %v", err)
	}
	if err := b.Apply(canceled, docstore.Mutation{Kind: "delete", Path: "jsonb/n0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply canceled: %v", err)
	}
	if err := b.DeleteBatch(canceled, []string{"jsonb/n0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteBatch canceled: %v", err)
	}
	if _, err := b.CurrentSequence(canceled, "seq/a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CurrentSequence canceled: %v", err)
	}
	if _, err := b.NextSequence(canceled, "seq/a", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("NextSequence canceled: %v", err)
	}
	if err := a.PutPNG(canceled, "r", "i", []byte{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutPNG canceled: %v", err)
	}
	if _, err := a.GetPNG(canceled, "r", "i"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPNG canceled: %v", err)
	}
	if err := a.PutFile(canceled, "k", "text/plain", []byte{1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutFile canceled: %v", err)
	}
	if _, err := a.GetFile(canceled, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetFile canceled: %v", err)
	}
	if err := a.DeleteFile(canceled, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteFile canceled: %v", err)
	}
}

func TestArtifactsTransientAndDeleteCommitThenError(t *testing.T) {
	ctx := context.Background()
	a := memstore.NewArtifacts(memstore.WithArtifactsFaults(memstore.FaultConfig{
		ArtifactErrProb: 1.0,
	}, rand.New(rand.NewPCG(1, 2))))

	if err := a.PutPNG(ctx, "r", "1", []byte{1}); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("PutPNG transient err: %v", err)
	}
	if _, err := a.GetPNG(ctx, "r", "1"); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("GetPNG transient err: %v", err)
	}
	if err := a.PutFile(ctx, "k", "text/plain", []byte{1}); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("PutFile transient err: %v", err)
	}
	if _, err := a.GetFile(ctx, "k"); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("GetFile transient err: %v", err)
	}
	if err := a.DeleteFile(ctx, "k"); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("DeleteFile transient err: %v", err)
	}
	if stats := a.FaultStats(); stats.ArtifactErrors != 5 {
		t.Fatalf("expected 5 ArtifactErrors, got %+v", stats)
	}

	// Verify transient PutFile did NOT persist the file.
	a.SetFaultsEnabled(false)
	if _, err := a.GetFile(ctx, "k"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expected k absent after transient PutFile error, got %v", err)
	}
	if err := a.PutFile(ctx, "k", "text/plain", []byte("ok")); err != nil {
		t.Fatalf("PutFile clean: %v", err)
	}

	// DeleteFile commit-then-error removes key durably then returns error.
	a.SetFaults(memstore.FaultConfig{CommitThenErrProb: 1.0}, rand.New(rand.NewPCG(3, 4)))
	if err := a.DeleteFile(ctx, "k"); !errors.Is(err, memstore.ErrCommitThenError) {
		t.Fatalf("DeleteFile commit-then-error: %v", err)
	}
	a.SetFaultsEnabled(false)
	if _, err := a.GetFile(ctx, "k"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("k should be durably deleted despite error, got %v", err)
	}
}

func runLeaseConformanceSuite(t *testing.T, b docstore.Backend, ls docstore.LeaseStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	client := docstore.New(b)

	var cleanupTasks []docstore.TaskKey
	var cleanupDocs []string
	trackTask := func(userID, runID string) {
		cleanupTasks = append(cleanupTasks, docstore.TaskKey{UserID: userID, RunID: runID})
	}
	trackDoc := func(paths ...string) {
		cleanupDocs = append(cleanupDocs, paths...)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, k := range cleanupTasks {
			_, _ = ls.DeleteTaskLease(bg, k.UserID, k.RunID, 0)
		}
		if len(cleanupDocs) > 0 {
			_ = b.DeleteBatch(bg, cleanupDocs)
		}
	})

	t.Run("ClaimTaskOrderingMutualExclusionAndExpiryTakeover", func(t *testing.T) {
		u1 := prefix + "_claim_u1"
		u2 := prefix + "_claim_u2"
		rEarly := prefix + "_run_early"
		rLate := prefix + "_run_late"
		rFuture := prefix + "_run_future"
		trackTask(u1, rEarly)
		trackTask(u2, rLate)
		trackTask(u1, rFuture)

		now := time.Now().UTC()
		if err := ls.WakeTask(ctx, u2, rLate, now.Add(-10*time.Second)); err != nil {
			t.Fatalf("WakeTask rLate: %v", err)
		}
		if err := ls.WakeTask(ctx, u1, rEarly, now.Add(-20*time.Second)); err != nil {
			t.Fatalf("WakeTask rEarly: %v", err)
		}
		if err := ls.WakeTask(ctx, u1, rFuture, now.Add(5*time.Minute)); err != nil {
			t.Fatalf("WakeTask rFuture: %v", err)
		}

		// ClaimTask must return earliest wake_at first (rEarly before rLate), skip future.
		c1, ok, err := ls.ClaimTask(ctx, "worker-1", 120*time.Millisecond)
		if err != nil || !ok {
			t.Fatalf("ClaimTask #1: ok=%v err=%v", ok, err)
		}
		if c1.UserID != u1 || c1.RunID != rEarly || c1.Owner != "worker-1" || c1.LeaseUntil == nil || c1.WakeSeq <= 0 {
			t.Fatalf("ClaimTask #1 unexpected: %+v", c1)
		}

		// ClaimTaskByID on already-leased task must fail (mutual exclusion).
		if _, ok, err := ls.ClaimTaskByID(ctx, u1, rEarly, "worker-2", time.Second); err != nil || ok {
			t.Fatalf("ClaimTaskByID while leased: ok=%v err=%v, want false/nil", ok, err)
		}

		// Second worker claims next available task (rLate) via SKIP LOCKED semantics.
		c2, ok, err := ls.ClaimTask(ctx, "worker-2", 5*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTask #2: ok=%v err=%v", ok, err)
		}
		if c2.UserID != u2 || c2.RunID != rLate || c2.Owner != "worker-2" {
			t.Fatalf("ClaimTask #2 unexpected: %+v", c2)
		}

		// Future task must not be claimable.
		if _, ok, err := ls.ClaimTaskByID(ctx, u1, rFuture, "worker-3", time.Second); err != nil || ok {
			t.Fatalf("ClaimTaskByID future task: ok=%v err=%v, want false/nil", ok, err)
		}

		// RenewTaskLeases extends only owned rows.
		renewed, err := ls.RenewTaskLeases(ctx, "worker-2", []docstore.TaskKey{
			{UserID: u2, RunID: rLate},
			{UserID: u1, RunID: rEarly}, // owned by worker-1, must NOT renew
		}, 10*time.Second)
		if err != nil || renewed != 1 {
			t.Fatalf("RenewTaskLeases: renewed=%d err=%v, want 1/nil", renewed, err)
		}

		// Wait for worker-1's short lease on rEarly to expire, then worker-2 takes over.
		time.Sleep(180 * time.Millisecond)
		taken, ok, err := ls.ClaimTaskByID(ctx, u1, rEarly, "worker-2", 5*time.Second)
		if err != nil || !ok {
			t.Fatalf("expiry takeover ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		if taken.Owner != "worker-2" || taken.WakeSeq != c1.WakeSeq {
			t.Fatalf("takeover unexpected: got %+v (want owner=worker-2 wake_seq=%d)", taken, c1.WakeSeq)
		}

		// Concurrent ClaimTaskByID race: only 1 of 2 workers may win an unowned task.
		rRace := prefix + "_run_race"
		trackTask(u1, rRace)
		if err := ls.WakeTask(ctx, u1, rRace, now.Add(-time.Second)); err != nil {
			t.Fatalf("WakeTask rRace: %v", err)
		}
		start := make(chan struct{})
		var wins atomic.Int32
		var wg sync.WaitGroup
		for _, owner := range []string{"race-w1", "race-w2"} {
			wg.Add(1)
			go func(o string) {
				defer wg.Done()
				<-start
				_, got, err := ls.ClaimTaskByID(ctx, u1, rRace, o, 5*time.Second)
				if err == nil && got {
					wins.Add(1)
				}
			}(owner)
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("concurrent ClaimTaskByID winners = %d, want 1", wins.Load())
		}

		// Clean up leases from this subtest so later ClaimTask tests don't see them.
		for _, k := range []docstore.TaskKey{{UserID: u1, RunID: rEarly}, {UserID: u2, RunID: rLate}, {UserID: u1, RunID: rFuture}, {UserID: u1, RunID: rRace}} {
			_, _ = ls.DeleteTaskLease(ctx, k.UserID, k.RunID, 0)
		}
	})

	t.Run("WakeSeqRaceGuardParkAndRefresh", func(t *testing.T) {
		u := prefix + "_seq_u"
		r := prefix + "_seq_r"
		trackTask(u, r)

		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("WakeTask: %v", err)
		}
		claimed, ok, err := ls.ClaimTaskByID(ctx, u, r, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}

		// Concurrent wake arrives while w1 holds lease: increments wake_seq, preserves owner.
		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("concurrent WakeTask: %v", err)
		}

		// ParkTask with stale wake_seq MUST fail (parked=false) and keep task runnable.
		parked, err := ls.ParkTask(ctx, u, r, "w1", claimed.WakeSeq, docstore.WaitQuestion)
		if err != nil || parked {
			t.Fatalf("ParkTask stale wake_seq: parked=%v err=%v, want false/nil", parked, err)
		}

		// RefreshTaskClaim must observe new wake_seq and extend lease for w1.
		refreshed, ok, err := ls.RefreshTaskClaim(ctx, u, r, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("RefreshTaskClaim: ok=%v err=%v", ok, err)
		}
		if refreshed.WakeSeq <= claimed.WakeSeq || !refreshed.Runnable || refreshed.Owner != "w1" {
			t.Fatalf("RefreshTaskClaim unexpected: %+v (prior seq=%d)", refreshed, claimed.WakeSeq)
		}

		// Wrong owner cannot refresh or park.
		if _, ok, err := ls.RefreshTaskClaim(ctx, u, r, "wrong-owner", 30*time.Second); err != nil || ok {
			t.Fatalf("RefreshTaskClaim wrong owner: ok=%v err=%v", ok, err)
		}
		if parked, err := ls.ParkTask(ctx, u, r, "wrong-owner", refreshed.WakeSeq, docstore.WaitQuestion); err != nil || parked {
			t.Fatalf("ParkTask wrong owner: parked=%v err=%v", parked, err)
		}

		// ParkTask with refreshed wake_seq succeeds.
		parked, err = ls.ParkTask(ctx, u, r, "w1", refreshed.WakeSeq, docstore.WaitQuestion)
		if err != nil || !parked {
			t.Fatalf("ParkTask refreshed wake_seq: parked=%v err=%v, want true/nil", parked, err)
		}

		rows, err := ls.ListTaskLeases(ctx, u)
		if err != nil || len(rows) != 1 {
			t.Fatalf("ListTaskLeases: rows=%+v err=%v", rows, err)
		}
		if rows[0].Runnable || rows[0].WaitingReason != docstore.WaitQuestion || rows[0].Owner != "" || rows[0].LeaseUntil != nil {
			t.Fatalf("parked row mismatch: %+v", rows[0])
		}
		_, _ = ls.DeleteTaskLease(ctx, u, r, 0)
	})

	t.Run("ReleaseTaskBackoffVsConcurrentWake", func(t *testing.T) {
		u := prefix + "_rel_u"
		r := prefix + "_rel_r"
		trackTask(u, r)

		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("WakeTask #1: %v", err)
		}
		c1, ok, err := ls.ClaimTaskByID(ctx, u, r, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID #1: ok=%v err=%v", ok, err)
		}

		// Matching wake_seq applies backoff nextWake.
		future := time.Now().UTC().Add(10 * time.Minute)
		if err := ls.ReleaseTask(ctx, u, r, "w1", c1.WakeSeq, future); err != nil {
			t.Fatalf("ReleaseTask backoff: %v", err)
		}
		if _, ok, err := ls.ClaimTaskByID(ctx, u, r, "w2", 30*time.Second); err != nil || ok {
			t.Fatalf("expected backed-off task not claimable, got ok=%v err=%v", ok, err)
		}

		// Now wake immediately, claim, inject concurrent immediate wake, then release with future backoff.
		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("WakeTask #2: %v", err)
		}
		c2, ok, err := ls.ClaimTaskByID(ctx, u, r, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID #2: ok=%v err=%v", ok, err)
		}
		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("concurrent WakeTask: %v", err)
		}
		if err := ls.ReleaseTask(ctx, u, r, "w1", c2.WakeSeq, future); err != nil {
			t.Fatalf("ReleaseTask after concurrent wake: %v", err)
		}

		// Mismatched wake_seq must NOT push WakeAt into the future: worker-2 must claim immediately!
		c3, ok, err := ls.ClaimTaskByID(ctx, u, r, "w2", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("expected immediate reclaim after concurrent wake, got ok=%v err=%v", ok, err)
		}
		if c3.WakeSeq <= c2.WakeSeq {
			t.Fatalf("expected wake_seq > %d, got %d", c2.WakeSeq, c3.WakeSeq)
		}
		_, _ = ls.DeleteTaskLease(ctx, u, r, 0)
	})

	t.Run("DeleteTaskLeaseGuardedVsUnconditional", func(t *testing.T) {
		u := prefix + "_del_u"
		r := prefix + "_del_r"
		trackTask(u, r)

		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("WakeTask: %v", err)
		}
		c1, ok, err := ls.ClaimTaskByID(ctx, u, r, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		// Concurrent wake bumps wake_seq.
		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("concurrent WakeTask: %v", err)
		}

		// Guarded delete with stale wakeSeq must return deleted=false and preserve row.
		deleted, err := ls.DeleteTaskLease(ctx, u, r, c1.WakeSeq)
		if err != nil || deleted {
			t.Fatalf("guarded DeleteTaskLease stale: deleted=%v err=%v, want false/nil", deleted, err)
		}
		rows, err := ls.ListTaskLeases(ctx, u)
		if err != nil || len(rows) != 1 {
			t.Fatalf("row should still exist after stale guarded delete: rows=%+v err=%v", rows, err)
		}

		// Guarded delete with current wakeSeq succeeds.
		deleted, err = ls.DeleteTaskLease(ctx, u, r, rows[0].WakeSeq)
		if err != nil || !deleted {
			t.Fatalf("guarded DeleteTaskLease current: deleted=%v err=%v, want true/nil", deleted, err)
		}

		// Unconditional delete (wakeSeq <= 0) removes row regardless of wakeSeq.
		if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
			t.Fatalf("WakeTask #2: %v", err)
		}
		deleted, err = ls.DeleteTaskLease(ctx, u, r, 0)
		if err != nil || !deleted {
			t.Fatalf("unconditional DeleteTaskLease: deleted=%v err=%v, want true/nil", deleted, err)
		}
		deleted, err = ls.DeleteTaskLease(ctx, u, r, 0)
		if err != nil || deleted {
			t.Fatalf("DeleteTaskLease missing row: deleted=%v err=%v, want false/nil", deleted, err)
		}
	})

	t.Run("EnsureTaskLeaseCreatesMissingAndPreservesParked", func(t *testing.T) {
		u := prefix + "_ens_u"
		r := prefix + "_ens_r"
		trackTask(u, r)

		created, err := ls.EnsureTaskLease(ctx, u, r, time.Time{})
		if err != nil || !created {
			t.Fatalf("EnsureTaskLease missing: created=%v err=%v, want true/nil", created, err)
		}
		created, err = ls.EnsureTaskLease(ctx, u, r, time.Time{})
		if err != nil || created {
			t.Fatalf("EnsureTaskLease duplicate: created=%v err=%v, want false/nil", created, err)
		}

		// Claim and park under WaitQuestion, then verify EnsureTaskLease does not disturb it.
		claimed, ok, err := ls.ClaimTaskByID(ctx, u, r, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		parked, err := ls.ParkTask(ctx, u, r, "w1", claimed.WakeSeq, docstore.WaitQuestion)
		if err != nil || !parked {
			t.Fatalf("ParkTask: parked=%v err=%v", parked, err)
		}
		created, err = ls.EnsureTaskLease(ctx, u, r, time.Time{})
		if err != nil || created {
			t.Fatalf("EnsureTaskLease on parked row: created=%v err=%v, want false/nil", created, err)
		}
		rows, err := ls.ListTaskLeases(ctx, u)
		if err != nil || len(rows) != 1 || rows[0].Runnable || rows[0].WaitingReason != docstore.WaitQuestion {
			t.Fatalf("EnsureTaskLease disturbed parked row: %+v err=%v", rows, err)
		}
		_, _ = ls.DeleteTaskLease(ctx, u, r, 0)
	})

	t.Run("WakeWaitingTasksScopedAndGlobal", func(t *testing.T) {
		uA := prefix + "_wait_uA"
		uB := prefix + "_wait_uB"
		rARouting := prefix + "_rA_routing"
		rAPaused := prefix + "_rA_paused"
		rBPaused := prefix + "_rB_paused"
		trackTask(uA, rARouting)
		trackTask(uA, rAPaused)
		trackTask(uB, rBPaused)

		parkTask := func(u, r, reason string) {
			t.Helper()
			if err := ls.WakeTask(ctx, u, r, time.Time{}); err != nil {
				t.Fatalf("WakeTask %s/%s: %v", u, r, err)
			}
			c, ok, err := ls.ClaimTaskByID(ctx, u, r, "w1", 30*time.Second)
			if err != nil || !ok {
				t.Fatalf("ClaimTaskByID %s/%s: ok=%v err=%v", u, r, ok, err)
			}
			parked, err := ls.ParkTask(ctx, u, r, "w1", c.WakeSeq, reason)
			if err != nil || !parked {
				t.Fatalf("ParkTask %s/%s: parked=%v err=%v", u, r, parked, err)
			}
		}

		parkTask(uA, rARouting, docstore.WaitRoutingPending)
		parkTask(uA, rAPaused, docstore.WaitPaused)
		parkTask(uB, rBPaused, docstore.WaitPaused)

		// Scoped wake for uA + routing_pending: only rARouting becomes runnable.
		if err := ls.WakeWaitingTasks(ctx, uA, docstore.WaitRoutingPending); err != nil {
			t.Fatalf("WakeWaitingTasks routing_pending: %v", err)
		}
		if _, ok, err := ls.ClaimTaskByID(ctx, uA, rARouting, "w2", 5*time.Second); err != nil || !ok {
			t.Fatalf("expected rARouting runnable: ok=%v err=%v", ok, err)
		}
		if _, ok, err := ls.ClaimTaskByID(ctx, uA, rAPaused, "w2", 5*time.Second); err != nil || ok {
			t.Fatalf("expected rAPaused still parked: ok=%v err=%v", ok, err)
		}
		if _, ok, err := ls.ClaimTaskByID(ctx, uB, rBPaused, "w2", 5*time.Second); err != nil || ok {
			t.Fatalf("expected rBPaused still parked: ok=%v err=%v", ok, err)
		}

		// Global unpause ("", paused): wakes both uA and uB paused tasks.
		if err := ls.WakeWaitingTasks(ctx, "", docstore.WaitPaused); err != nil {
			t.Fatalf("WakeWaitingTasks global paused: %v", err)
		}
		if _, ok, err := ls.ClaimTaskByID(ctx, uA, rAPaused, "w2", 5*time.Second); err != nil || !ok {
			t.Fatalf("expected rAPaused runnable after global unpause: ok=%v err=%v", ok, err)
		}
		if _, ok, err := ls.ClaimTaskByID(ctx, uB, rBPaused, "w2", 5*time.Second); err != nil || !ok {
			t.Fatalf("expected rBPaused runnable after global unpause: ok=%v err=%v", ok, err)
		}
		_, _ = ls.DeleteTaskLease(ctx, uA, rARouting, 0)
		_, _ = ls.DeleteTaskLease(ctx, uA, rAPaused, 0)
		_, _ = ls.DeleteTaskLease(ctx, uB, rBPaused, 0)
	})

	t.Run("SameTxWakeWriterAtomicityAndListenWakes", func(t *testing.T) {
		u := prefix + "_txwake_u"
		rAbort := prefix + "_txwake_abort"
		rCommit := prefix + "_txwake_commit"
		trackTask(u, rAbort)
		trackTask(u, rCommit)

		col := client.Collection(prefix + "/txwake")
		docAbort := col.Doc("abort")
		docCommit := col.Doc("commit")
		trackDoc(docAbort.Path, docCommit.Path)

		var (
			evMu     sync.Mutex
			received []docstore.WakeEvent
			evSignal = make(chan struct{}, 32)
		)
		listenCtx, cancelListen := context.WithCancel(ctx)
		defer cancelListen()
		listenErr := make(chan error, 1)
		hasListener := false
		if wl, ok := any(b).(docstore.WakeListener); ok {
			hasListener = true
			onEv := func(ev docstore.WakeEvent) {
				evMu.Lock()
				received = append(received, ev)
				evMu.Unlock()
				select {
				case evSignal <- struct{}{}:
				default:
				}
			}
			type wakeReadyListener interface {
				ListenWakesReady(context.Context, func(docstore.WakeEvent), chan<- struct{}) error
			}
			type wakeSubWaiter interface {
				WaitForWakeSubscribers(context.Context, int) error
			}
			switch hook := any(b).(type) {
			case wakeReadyListener:
				ready := make(chan struct{})
				go func() {
					listenErr <- hook.ListenWakesReady(listenCtx, onEv, ready)
				}()
				select {
				case <-ready:
				case err := <-listenErr:
					t.Fatalf("ListenWakesReady exited before subscribing: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatalf("timed out waiting for ListenWakes subscription")
				}
			case wakeSubWaiter:
				go func() {
					listenErr <- wl.ListenWakes(listenCtx, onEv)
				}()
				waitCtx, cancelWait := context.WithTimeout(ctx, 3*time.Second)
				err := hook.WaitForWakeSubscribers(waitCtx, 1)
				cancelWait()
				if err != nil {
					t.Fatalf("WaitForWakeSubscribers: %v", err)
				}
			default:
				go func() {
					listenErr <- wl.ListenWakes(listenCtx, onEv)
				}()
				time.Sleep(25 * time.Millisecond)
			}
		}

		// 1. Rolled-back transaction must create NO doc, NO wake rows, and emit NO wake events.
		sentinel := errors.New("rollback wake tx")
		err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if !tx.HasWakeWriter() {
				t.Fatalf("expected transaction to support WakeWriter")
			}
			if err := tx.Create(docAbort, map[string]any{"v": 1}); err != nil {
				return err
			}
			if err := tx.WakeTask(u, rAbort, time.Time{}); err != nil {
				return err
			}
			if err := tx.WakeUser(u, docstore.LaneConversation, time.Time{}); err != nil {
				return err
			}
			if err := tx.NotifyPause(); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("aborted tx err = %v, want %v", err, sentinel)
		}
		if _, err := docAbort.Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("docAbort should not exist after rollback: %v", err)
		}
		if leases, err := ls.ListTaskLeases(ctx, u); err != nil || len(leases) != 0 {
			t.Fatalf("aborted tx leaked task lease: %+v err=%v", leases, err)
		}
		if wakes, err := ls.ListUserWakes(ctx, u); err != nil || len(wakes) != 0 {
			t.Fatalf("aborted tx leaked user wake: %+v err=%v", wakes, err)
		}
		if hasListener {
			// Intentional short negative-assertion window confirming rollback emitted no wake event.
			time.Sleep(30 * time.Millisecond)
			evMu.Lock()
			for _, ev := range received {
				if ev.Payload == docstore.TaskWakePayload(u, rAbort) || ev.Payload == docstore.UserWakePayload(u, docstore.LaneConversation) {
					t.Fatalf("aborted tx emitted wake event: %+v", ev)
				}
			}
			evMu.Unlock()
		}

		// 2. Committed transaction creates doc + lease + user wake AND emits post-commit events.
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.Create(docCommit, map[string]any{"v": 2}); err != nil {
				return err
			}
			if err := tx.WakeTask(u, rCommit, time.Time{}); err != nil {
				return err
			}
			if err := tx.WakeUser(u, docstore.LaneConversation, time.Time{}); err != nil {
				return err
			}
			return tx.NotifyPause()
		})
		if err != nil {
			t.Fatalf("committed tx failed: %v", err)
		}
		if snap, err := docCommit.Get(ctx); err != nil || snap.Data()["v"] != int64(2) {
			t.Fatalf("docCommit missing: snap=%v err=%v", snap, err)
		}
		if leases, err := ls.ListTaskLeases(ctx, u); err != nil || len(leases) != 1 || leases[0].RunID != rCommit {
			t.Fatalf("committed task lease missing: %+v err=%v", leases, err)
		}
		if wakes, err := ls.ListUserWakes(ctx, u); err != nil || len(wakes) != 1 || wakes[0].Lane != docstore.LaneConversation {
			t.Fatalf("committed user wake missing: %+v err=%v", wakes, err)
		}

		if hasListener {
			wantTask := docstore.TaskWakePayload(u, rCommit)
			wantConv := docstore.UserWakePayload(u, docstore.LaneConversation)
			deadline := time.After(3 * time.Second)
			for {
				evMu.Lock()
				var gotTask, gotConv, gotPause bool
				for _, ev := range received {
					if ev.Channel != docstore.WakeChannel {
						continue
					}
					switch ev.Payload {
					case wantTask:
						gotTask = true
					case wantConv:
						gotConv = true
					case docstore.WakePayloadPause:
						gotPause = true
					}
				}
				evMu.Unlock()
				if gotTask && gotConv && gotPause {
					break
				}
				select {
				case <-evSignal:
				case <-deadline:
					evMu.Lock()
					t.Fatalf("timed out waiting for post-commit wake events (gotTask=%v gotConv=%v gotPause=%v received=%+v)", gotTask, gotConv, gotPause, received)
					evMu.Unlock()
				}
			}
			cancelListen()
			if err := <-listenErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("ListenWakes exit err = %v, want context.Canceled", err)
			}
		}

		// Clean up user wake by claiming and parking, and delete task lease.
		if uw, ok, _ := ls.ClaimUserWake(ctx, docstore.LaneConversation, "cleanup", 5*time.Second); ok && uw.UserID == u {
			_, _ = ls.ParkUserWake(ctx, u, docstore.LaneConversation, "cleanup", uw.WakeSeq)
		}
		_, _ = ls.DeleteTaskLease(ctx, u, rCommit, 0)
	})

	t.Run("UserWakeConversationAndDelayedSchedulerLanes", func(t *testing.T) {
		u := prefix + "_uw_u"
		now := time.Now().UTC()
		future := now.Add(10 * time.Minute)

		if err := ls.WakeUser(ctx, u, docstore.LaneConversation, now.Add(-24*time.Hour)); err != nil {
			t.Fatalf("WakeUser conversation: %v", err)
		}
		if err := ls.WakeUser(ctx, u, docstore.LaneScheduler, future); err != nil {
			t.Fatalf("WakeUser scheduler future: %v", err)
		}

		// Scheduler wake is in the future -> must not be claimed.
		if uw, ok, err := ls.ClaimUserWake(ctx, docstore.LaneScheduler, "w1", 30*time.Second); err != nil {
			t.Fatalf("ClaimUserWake scheduler future: %v", err)
		} else if ok && uw.UserID == u {
			t.Fatalf("future scheduler wake was claimed early: %+v", uw)
		}

		// Conversation wake is due -> claim, renew, test stale ParkUserWake, then park.
		cw, ok, err := ls.ClaimUserWake(ctx, docstore.LaneConversation, "w1", 30*time.Second)
		if err != nil || !ok || cw.UserID != u || cw.Lane != docstore.LaneConversation {
			t.Fatalf("ClaimUserWake conversation: uw=%+v ok=%v err=%v", cw, ok, err)
		}
		if ok, err := ls.RenewUserWake(ctx, u, docstore.LaneConversation, "w1", 30*time.Second); err != nil || !ok {
			t.Fatalf("RenewUserWake owner: ok=%v err=%v", ok, err)
		}
		if ok, err := ls.RenewUserWake(ctx, u, docstore.LaneConversation, "other", 30*time.Second); err != nil || ok {
			t.Fatalf("RenewUserWake wrong owner: ok=%v err=%v", ok, err)
		}

		// Concurrent WakeUser bumps wake_seq -> ParkUserWake with old seq must fail.
		if err := ls.WakeUser(ctx, u, docstore.LaneConversation, time.Time{}); err != nil {
			t.Fatalf("concurrent WakeUser: %v", err)
		}
		if parked, err := ls.ParkUserWake(ctx, u, docstore.LaneConversation, "w1", cw.WakeSeq); err != nil || parked {
			t.Fatalf("ParkUserWake stale seq: parked=%v err=%v, want false/nil", parked, err)
		}

		// ReleaseUserWake with stale wake_seq and a LATER nextWake must preserve the earlier concurrent WakeAt!
		if err := ls.ReleaseUserWake(ctx, u, docstore.LaneConversation, "w1", cw.WakeSeq, future); err != nil {
			t.Fatalf("ReleaseUserWake stale: %v", err)
		}
		cw2, ok, err := ls.ClaimUserWake(ctx, docstore.LaneConversation, "w2", 30*time.Second)
		if err != nil || !ok || cw2.UserID != u || cw2.WakeSeq <= cw.WakeSeq {
			t.Fatalf("expected immediate reclaim preserving earlier wake: uw=%+v ok=%v err=%v", cw2, ok, err)
		}

		// Now park cleanly with current wake_seq.
		if parked, err := ls.ParkUserWake(ctx, u, docstore.LaneConversation, "w2", cw2.WakeSeq); err != nil || !parked {
			t.Fatalf("ParkUserWake current seq: parked=%v err=%v, want true/nil", parked, err)
		}

		// Wake scheduler lane now, claim, release to future, and verify it's delayed again.
		if err := ls.WakeUser(ctx, u, docstore.LaneScheduler, now.Add(-24*time.Hour)); err != nil {
			t.Fatalf("WakeUser scheduler due: %v", err)
		}
		sw, ok, err := ls.ClaimUserWake(ctx, docstore.LaneScheduler, "w1", 30*time.Second)
		if err != nil || !ok || sw.UserID != u || sw.Lane != docstore.LaneScheduler {
			t.Fatalf("ClaimUserWake scheduler due: uw=%+v ok=%v err=%v", sw, ok, err)
		}
		if err := ls.ReleaseUserWake(ctx, u, docstore.LaneScheduler, "w1", sw.WakeSeq, future); err != nil {
			t.Fatalf("ReleaseUserWake scheduler future: %v", err)
		}
		if uw, ok, err := ls.ClaimUserWake(ctx, docstore.LaneScheduler, "w2", 30*time.Second); err != nil {
			t.Fatalf("ClaimUserWake after release: %v", err)
		} else if ok && uw.UserID == u {
			t.Fatalf("scheduler wake claimed before future wake_at: %+v", uw)
		}

		wakes, err := ls.ListUserWakes(ctx, u)
		if err != nil || len(wakes) != 2 {
			t.Fatalf("ListUserWakes: %+v err=%v", wakes, err)
		}
		if wakes[0].Lane != docstore.LaneConversation || wakes[1].Lane != docstore.LaneScheduler {
			t.Fatalf("ListUserWakes not sorted by (UserID, Lane): %+v", wakes)
		}
	})

	t.Run("WakeWaitingTasksBetweenClaimAndPark", func(t *testing.T) {
		u := prefix + "_racewait_u"
		rRouting := "run-routing-race"
		rPause := "run-pause-race"
		t.Cleanup(func() {
			_, _ = ls.DeleteTaskLease(context.Background(), u, rRouting, 0)
			_, _ = ls.DeleteTaskLease(context.Background(), u, rPause, 0)
		})

		// Routing-cleared race: worker claims task, WakeWaitingTasks(user, routing_pending)
		// fires while the worker is still in-flight (runnable=true, owner="w1"), then worker
		// tries to ParkTask with its captured wake_seq -> MUST fail (parked=false) so wake is never lost!
		if err := ls.WakeTask(ctx, u, rRouting, time.Time{}); err != nil {
			t.Fatalf("WakeTask routing: %v", err)
		}
		claimed, ok, err := ls.ClaimTaskByID(ctx, u, rRouting, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID routing: ok=%v err=%v", ok, err)
		}
		if err := ls.WakeWaitingTasks(ctx, u, docstore.WaitRoutingPending); err != nil {
			t.Fatalf("WakeWaitingTasks routing_pending: %v", err)
		}
		parked, err := ls.ParkTask(ctx, u, rRouting, "w1", claimed.WakeSeq, docstore.WaitRoutingPending)
		if err != nil {
			t.Fatalf("ParkTask after WakeWaitingTasks routing_pending: %v", err)
		}
		if parked {
			t.Fatalf("LOST WAKE: ParkTask succeeded with stale wake_seq=%d after WakeWaitingTasks(routing_pending)", claimed.WakeSeq)
		}

		// Global unpause race: worker claims task, global WakeWaitingTasks("", paused) fires
		// while worker is in-flight, then worker tries to ParkTask(..., paused) -> MUST fail!
		if err := ls.WakeTask(ctx, u, rPause, time.Time{}); err != nil {
			t.Fatalf("WakeTask pause: %v", err)
		}
		claimedPause, ok, err := ls.ClaimTaskByID(ctx, u, rPause, "w2", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID pause: ok=%v err=%v", ok, err)
		}
		if err := ls.WakeWaitingTasks(ctx, "", docstore.WaitPaused); err != nil {
			t.Fatalf("WakeWaitingTasks global paused: %v", err)
		}
		parked, err = ls.ParkTask(ctx, u, rPause, "w2", claimedPause.WakeSeq, docstore.WaitPaused)
		if err != nil {
			t.Fatalf("ParkTask after global WakeWaitingTasks(paused): %v", err)
		}
		if parked {
			t.Fatalf("LOST WAKE: ParkTask succeeded with stale wake_seq=%d after global WakeWaitingTasks(paused)", claimedPause.WakeSeq)
		}

		// Empty owner must never park an unowned or owned task.
		if parked, err := ls.ParkTask(ctx, u, rPause, "", claimedPause.WakeSeq+1, docstore.WaitPaused); err != nil || parked {
			t.Fatalf("ParkTask with empty owner: parked=%v err=%v, want false/nil", parked, err)
		}

		// Unclaimed runnable row (runnable=true, owner="", even with future wake_at) MUST also
		// be matched by WakeWaitingTasks on both Postgres and memstore: wake_seq increments and
		// wake_at is clamped to <= now so it becomes immediately claimable.
		rUnclaimed := "run-unclaimed-runnable"
		t.Cleanup(func() {
			_, _ = ls.DeleteTaskLease(context.Background(), u, rUnclaimed, 0)
		})
		if err := ls.WakeTask(ctx, u, rUnclaimed, time.Now().UTC().Add(10*time.Minute)); err != nil {
			t.Fatalf("WakeTask unclaimed future: %v", err)
		}
		if _, ok, err := ls.ClaimTaskByID(ctx, u, rUnclaimed, "w3", 30*time.Second); err != nil || ok {
			t.Fatalf("future unclaimed task should not be claimable yet: ok=%v err=%v", ok, err)
		}
		if err := ls.WakeWaitingTasks(ctx, u, docstore.WaitRoutingPending); err != nil {
			t.Fatalf("WakeWaitingTasks on unclaimed runnable row: %v", err)
		}
		cUnclaimed, ok, err := ls.ClaimTaskByID(ctx, u, rUnclaimed, "w3", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("expected unclaimed runnable row to become immediately claimable after WakeWaitingTasks: ok=%v err=%v", ok, err)
		}
		if cUnclaimed.WakeSeq < 2 {
			t.Fatalf("expected unclaimed runnable row wake_seq >= 2 after WakeWaitingTasks, got %d", cUnclaimed.WakeSeq)
		}
	})

	t.Run("ScopedLeaseStoreClaimForUser", func(t *testing.T) {
		scoped, ok := any(ls).(docstore.ScopedLeaseStore)
		if !ok {
			t.Fatalf("LeaseStore %T does not implement docstore.ScopedLeaseStore", ls)
		}
		u1 := prefix + "_scoped_u1"
		u2 := prefix + "_scoped_u2"
		rU1Early := prefix + "_s_u1_early"
		rU1Late := prefix + "_s_u1_late"
		rU1Future := prefix + "_s_u1_future"
		rU2Earlier := prefix + "_s_u2_earlier"
		trackTask(u1, rU1Early)
		trackTask(u1, rU1Late)
		trackTask(u1, rU1Future)
		trackTask(u2, rU2Earlier)

		if _, _, err := scoped.ClaimTaskForUser(ctx, "", "w1", time.Second); err == nil {
			t.Fatal("ClaimTaskForUser with empty userID = nil, want error")
		}
		if _, _, err := scoped.ClaimTaskForUser(ctx, u1, "", time.Second); err == nil {
			t.Fatal("ClaimTaskForUser with empty owner = nil, want error")
		}
		if _, _, err := scoped.ClaimUserWakeForUser(ctx, "", docstore.LaneConversation, "w1", time.Second); err == nil {
			t.Fatal("ClaimUserWakeForUser with empty userID = nil, want error")
		}
		if _, _, err := scoped.ClaimUserWakeForUser(ctx, u1, "", "w1", time.Second); err == nil {
			t.Fatal("ClaimUserWakeForUser with empty lane = nil, want error")
		}
		if _, _, err := scoped.ClaimUserWakeForUser(ctx, u1, docstore.LaneConversation, "", time.Second); err == nil {
			t.Fatal("ClaimUserWakeForUser with empty owner = nil, want error")
		}

		now := time.Now().UTC()
		if err := ls.WakeTask(ctx, u2, rU2Earlier, now.Add(-60*time.Second)); err != nil {
			t.Fatalf("WakeTask u2: %v", err)
		}
		if err := ls.WakeTask(ctx, u1, rU1Late, now.Add(-10*time.Second)); err != nil {
			t.Fatalf("WakeTask u1 late: %v", err)
		}
		if err := ls.WakeTask(ctx, u1, rU1Early, now.Add(-20*time.Second)); err != nil {
			t.Fatalf("WakeTask u1 early: %v", err)
		}
		if err := ls.WakeTask(ctx, u1, rU1Future, now.Add(5*time.Minute)); err != nil {
			t.Fatalf("WakeTask u1 future: %v", err)
		}

		// ClaimTaskForUser(u1) must skip u2's even-earlier task and claim u1's earliest due task.
		c1, ok, err := scoped.ClaimTaskForUser(ctx, u1, "w1", 120*time.Millisecond)
		if err != nil || !ok || c1.UserID != u1 || c1.RunID != rU1Early {
			t.Fatalf("ClaimTaskForUser(u1) #1 = (%+v, %v, %v), want %s/%s", c1, ok, err, u1, rU1Early)
		}
		c2, ok, err := scoped.ClaimTaskForUser(ctx, u1, "w1", 5*time.Second)
		if err != nil || !ok || c2.UserID != u1 || c2.RunID != rU1Late {
			t.Fatalf("ClaimTaskForUser(u1) #2 = (%+v, %v, %v), want %s/%s", c2, ok, err, u1, rU1Late)
		}
		// Only u1 future and u2 due remain -> u1 must return ok=false.
		if c3, ok, err := scoped.ClaimTaskForUser(ctx, u1, "w1", 5*time.Second); err != nil || ok {
			t.Fatalf("ClaimTaskForUser(u1) #3 = (%+v, %v, %v), want ok=false", c3, ok, err)
		}
		// Lease expiry takeover for u1.
		time.Sleep(180 * time.Millisecond)
		taken, ok, err := scoped.ClaimTaskForUser(ctx, u1, "w2", 5*time.Second)
		if err != nil || !ok || taken.UserID != u1 || taken.RunID != rU1Early || taken.Owner != "w2" {
			t.Fatalf("ClaimTaskForUser(u1) expiry takeover = (%+v, %v, %v)", taken, ok, err)
		}
		// u2's task remained unclaimed and can be claimed for u2.
		cu2, ok, err := scoped.ClaimTaskForUser(ctx, u2, "w2", 5*time.Second)
		if err != nil || !ok || cu2.UserID != u2 || cu2.RunID != rU2Earlier {
			t.Fatalf("ClaimTaskForUser(u2) = (%+v, %v, %v), want %s/%s", cu2, ok, err, u2, rU2Earlier)
		}

		// User-wake scoping: ClaimUserWakeForUser ignores u2's due wakes and future u1 wakes.
		if err := ls.WakeUser(ctx, u2, docstore.LaneConversation, now.Add(-60*time.Second)); err != nil {
			t.Fatalf("WakeUser u2 conv: %v", err)
		}
		if err := ls.WakeUser(ctx, u2, docstore.LaneScheduler, now.Add(-60*time.Second)); err != nil {
			t.Fatalf("WakeUser u2 sched: %v", err)
		}
		if err := ls.WakeUser(ctx, u1, docstore.LaneConversation, now.Add(-5*time.Second)); err != nil {
			t.Fatalf("WakeUser u1 conv: %v", err)
		}
		if err := ls.WakeUser(ctx, u1, docstore.LaneScheduler, now.Add(5*time.Minute)); err != nil {
			t.Fatalf("WakeUser u1 sched future: %v", err)
		}

		uw1, ok, err := scoped.ClaimUserWakeForUser(ctx, u1, docstore.LaneConversation, "w1", 120*time.Millisecond)
		if err != nil || !ok || uw1.UserID != u1 || uw1.Lane != docstore.LaneConversation {
			t.Fatalf("ClaimUserWakeForUser(u1, conv) = (%+v, %v, %v)", uw1, ok, err)
		}
		// Mutual exclusion while leased.
		if uwDup, ok, err := scoped.ClaimUserWakeForUser(ctx, u1, docstore.LaneConversation, "w2", 5*time.Second); err != nil || ok {
			t.Fatalf("ClaimUserWakeForUser(u1, conv) while leased = (%+v, %v, %v), want ok=false", uwDup, ok, err)
		}
		// Future u1 scheduler wake must not claim (and must not claim u2's due scheduler wake).
		if uwSched, ok, err := scoped.ClaimUserWakeForUser(ctx, u1, docstore.LaneScheduler, "w1", 5*time.Second); err != nil || ok {
			t.Fatalf("ClaimUserWakeForUser(u1, sched future) = (%+v, %v, %v), want ok=false", uwSched, ok, err)
		}
		// Expiry takeover on u1 conversation lane.
		time.Sleep(180 * time.Millisecond)
		uwTakeover, ok, err := scoped.ClaimUserWakeForUser(ctx, u1, docstore.LaneConversation, "w2", 5*time.Second)
		if err != nil || !ok || uwTakeover.UserID != u1 || uwTakeover.Owner != "w2" {
			t.Fatalf("ClaimUserWakeForUser(u1, conv) takeover = (%+v, %v, %v)", uwTakeover, ok, err)
		}
		// Clean up u1/u2 user wakes.
		_, _ = ls.ParkUserWake(ctx, u1, docstore.LaneConversation, "w2", uwTakeover.WakeSeq)
		if uw2, ok, err := scoped.ClaimUserWakeForUser(ctx, u2, docstore.LaneConversation, "w2", 5*time.Second); err != nil || !ok {
			t.Fatalf("ClaimUserWakeForUser(u2, conv) = (%+v, %v, %v)", uw2, ok, err)
		} else {
			_, _ = ls.ParkUserWake(ctx, u2, docstore.LaneConversation, "w2", uw2.WakeSeq)
		}
		if uw2s, ok, err := scoped.ClaimUserWakeForUser(ctx, u2, docstore.LaneScheduler, "w2", 5*time.Second); err != nil || !ok {
			t.Fatalf("ClaimUserWakeForUser(u2, sched) = (%+v, %v, %v)", uw2s, ok, err)
		} else {
			_, _ = ls.ParkUserWake(ctx, u2, docstore.LaneScheduler, "w2", uw2s.WakeSeq)
		}
		for _, k := range []docstore.TaskKey{{UserID: u1, RunID: rU1Early}, {UserID: u1, RunID: rU1Late}, {UserID: u1, RunID: rU1Future}, {UserID: u2, RunID: rU2Earlier}} {
			_, _ = ls.DeleteTaskLease(ctx, k.UserID, k.RunID, 0)
		}
	})
}

func TestMemstoreLeases_ClockSnapshotPostCommitAndConflicts(t *testing.T) {
	ctx := context.Background()

	t.Run("DeterministicClockBoundariesAndNoDocTxConflict", func(t *testing.T) {
		var mu sync.Mutex
		now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
		clock := func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}
		advance := func(d time.Duration) {
			mu.Lock()
			now = now.Add(d)
			mu.Unlock()
		}

		b := memstore.New(memstore.WithClock(clock))
		client := docstore.New(b)

		// Schedule a task 10s in the future.
		if err := b.WakeTask(ctx, "u1", "r1", clock().Add(10*time.Second)); err != nil {
			t.Fatalf("WakeTask: %v", err)
		}
		if _, ok, err := b.ClaimTask(ctx, "w1", 30*time.Second); err != nil || ok {
			t.Fatalf("should not claim future task: ok=%v err=%v", ok, err)
		}

		// Exact WakeAt == now IS claimable.
		advance(10 * time.Second)
		claimed, ok, err := b.ClaimTask(ctx, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("expected claim at WakeAt == now: ok=%v err=%v", ok, err)
		}

		// 1ns before LeaseUntil is still active (LeaseUntil > now): not claimable, renew succeeds.
		advance(30*time.Second - time.Nanosecond)
		if _, ok, err := b.ClaimTask(ctx, "w2", 30*time.Second); err != nil || ok {
			t.Fatalf("should not take over before LeaseUntil: ok=%v err=%v", ok, err)
		}
		if n, err := b.RenewTaskLeases(ctx, "w1", []docstore.TaskKey{{UserID: "u1", RunID: "r1"}}, 30*time.Second); err != nil || n != 1 {
			t.Fatalf("expected active RenewTaskLeases to succeed: n=%d err=%v", n, err)
		}

		// Exact LeaseUntil == now IS expired (lease_until <= now()): renew/refresh fail and takeover succeeds!
		advance(30 * time.Second)
		if n, err := b.RenewTaskLeases(ctx, "w1", []docstore.TaskKey{{UserID: "u1", RunID: "r1"}}, 30*time.Second); err != nil || n != 0 {
			t.Fatalf("expected expired RenewTaskLeases to fail: n=%d err=%v", n, err)
		}
		if _, ok, err := b.RefreshTaskClaim(ctx, "u1", "r1", "w1", 30*time.Second); err != nil || ok {
			t.Fatalf("expected expired RefreshTaskClaim to fail: ok=%v err=%v", ok, err)
		}
		taken, ok, err := b.ClaimTask(ctx, "w2", 30*time.Second)
		if err != nil || !ok || taken.Owner != "w2" || taken.WakeSeq != claimed.WakeSeq {
			t.Fatalf("expected takeover at LeaseUntil == now: got=%+v ok=%v err=%v", taken, ok, err)
		}

		// READ COMMITTED LeaseStore operations during an open docstore tx must NEVER cause ErrTxConflict.
		docRef := client.Collection("c").Doc("d1")
		if _, err := docRef.Create(ctx, map[string]any{"v": 1}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		attempts := 0
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			attempts++
			if _, err := tx.Get(docRef); err != nil {
				return err
			}
			// Perform LeaseStore reads/writes mid-transaction.
			if err := b.WakeTask(ctx, "u1", "r2", time.Time{}); err != nil {
				return err
			}
			if _, ok, err := b.ClaimTaskByID(ctx, "u1", "r2", "w3", 30*time.Second); err != nil || !ok {
				return fmt.Errorf("ClaimTaskByID mid-tx: ok=%v err=%v", ok, err)
			}
			return tx.Update(docRef, []docstore.Update{{Path: "v", Value: 2}})
		}, docstore.MaxAttempts(1))
		if err != nil || attempts != 1 {
			t.Fatalf("docstore tx conflicted with READ COMMITTED LeaseStore op: attempts=%d err=%v", attempts, err)
		}
	})

	t.Run("PostCommitNotificationTimingAndTxConflictRollback", func(t *testing.T) {
		b := memstore.New()
		client := docstore.New(b)

		evCh := make(chan docstore.WakeEvent, 16)
		listenCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			_ = b.ListenWakes(listenCtx, func(ev docstore.WakeEvent) {
				evCh <- ev
			})
		}()
		waitCtx, cancelWait := context.WithTimeout(ctx, time.Second)
		if err := b.WaitForWakeSubscribers(waitCtx, 1); err != nil {
			cancelWait()
			t.Fatalf("WaitForWakeSubscribers: %v", err)
		}
		cancelWait()

		// Verify WakeEvent is NOT emitted while tx callback is still executing.
		err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.WakeTask("u1", "r1", time.Time{}); err != nil {
				return err
			}
			select {
			case ev := <-evCh:
				return fmt.Errorf("wake event emitted BEFORE commit: %+v", ev)
			case <-time.After(20 * time.Millisecond):
			}
			return nil
		})
		if err != nil {
			t.Fatalf("RunTransaction: %v", err)
		}
		select {
		case ev := <-evCh:
			if ev.Payload != docstore.TaskWakePayload("u1", "r1") {
				t.Fatalf("unexpected post-commit event: %+v", ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for post-commit wake event")
		}

		// Forced ErrTxConflict on commit must NOT persist wake rows or emit events.
		b.SetFaults(memstore.FaultConfig{TxConflictProb: 1.0}, rand.New(rand.NewPCG(1, 2)))
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.WakeTask("u2", "r_conflict", time.Time{}); err != nil {
				return err
			}
			return tx.WakeUser("u2", docstore.LaneConversation, time.Time{})
		}, docstore.MaxAttempts(2))
		if !errors.Is(err, memstore.ErrTxConflict) {
			t.Fatalf("expected ErrTxConflict, got %v", err)
		}
		b.SetFaultsEnabled(false)
		if leases, _ := b.ListTaskLeases(ctx, "u2"); len(leases) != 0 {
			t.Fatalf("conflicted tx leaked task leases: %+v", leases)
		}
		if wakes, _ := b.ListUserWakes(ctx, "u2"); len(wakes) != 0 {
			t.Fatalf("conflicted tx leaked user wakes: %+v", wakes)
		}
		select {
		case ev := <-evCh:
			t.Fatalf("conflicted tx emitted wake event: %+v", ev)
		case <-time.After(25 * time.Millisecond):
		}

		// CommitThenErr on a wake-only tx commits durably and still returns ErrCommitThenError.
		b.SetFaults(memstore.FaultConfig{CommitThenErrProb: 1.0}, rand.New(rand.NewPCG(3, 4)))
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			return tx.WakeTask("u3", "r_cte", time.Time{})
		})
		if !errors.Is(err, memstore.ErrCommitThenError) {
			t.Fatalf("expected ErrCommitThenError, got %v", err)
		}
		b.SetFaultsEnabled(false)
		if leases, _ := b.ListTaskLeases(ctx, "u3"); len(leases) != 1 || leases[0].RunID != "r_cte" {
			t.Fatalf("wake-only CommitThenErr did not commit lease: %+v", leases)
		}
	})

	t.Run("WakeWaitingTasksCoalescesAbove32Rows", func(t *testing.T) {
		b := memstore.New()
		evCh := make(chan docstore.WakeEvent, 64)
		listenCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			_ = b.ListenWakes(listenCtx, func(ev docstore.WakeEvent) {
				evCh <- ev
			})
		}()
		waitCtx, cancelWait := context.WithTimeout(ctx, time.Second)
		if err := b.WaitForWakeSubscribers(waitCtx, 1); err != nil {
			cancelWait()
			t.Fatalf("WaitForWakeSubscribers: %v", err)
		}
		cancelWait()

		for i := 0; i < 33; i++ {
			runID := fmt.Sprintf("r%02d", i)
			_ = b.WakeTask(ctx, "uBig", runID, time.Time{})
			c, _, _ := b.ClaimTaskByID(ctx, "uBig", runID, "w1", 30*time.Second)
			_, _ = b.ParkTask(ctx, "uBig", runID, "w1", c.WakeSeq, docstore.WaitRoutingPending)
		}
		for i := 0; i < 33; i++ {
			<-evCh
		}

		if err := b.WakeWaitingTasks(ctx, "uBig", docstore.WaitRoutingPending); err != nil {
			t.Fatalf("WakeWaitingTasks: %v", err)
		}
		select {
		case ev := <-evCh:
			if ev.Payload != docstore.UserTasksWakePayload("uBig") {
				t.Fatalf("expected coalesced payload %q, got %q", docstore.UserTasksWakePayload("uBig"), ev.Payload)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for coalesced wake event")
		}
	})

	t.Run("SnapshotAndDigestIncludeLeasesAndWakesFaultFree", func(t *testing.T) {
		fixed := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		clock := func() time.Time { return fixed }

		b1 := memstore.New(memstore.WithClock(clock))
		b2 := memstore.New(memstore.WithClock(clock))

		// Insert leases and wakes in opposite orders; digests must match.
		_ = b1.WakeTask(ctx, "u1", "r1", fixed)
		_ = b1.WakeTask(ctx, "u2", "r2", fixed)
		_ = b1.WakeUser(ctx, "u1", docstore.LaneConversation, fixed)
		_ = b1.WakeUser(ctx, "u1", docstore.LaneScheduler, fixed.Add(time.Minute))

		_ = b2.WakeUser(ctx, "u1", docstore.LaneScheduler, fixed.Add(time.Minute))
		_ = b2.WakeUser(ctx, "u1", docstore.LaneConversation, fixed)
		_ = b2.WakeTask(ctx, "u2", "r2", fixed)
		_ = b2.WakeTask(ctx, "u1", "r1", fixed)

		if b1.Digest() != b2.Digest() {
			t.Fatalf("digests diverged across insertion orders: %s vs %s", b1.Digest(), b2.Digest())
		}

		// Claiming a task changes the digest; mutating Snapshot must not mutate Backend.
		beforeClaim := b1.Digest()
		if _, ok, err := b1.ClaimTaskByID(ctx, "u1", "r1", "w1", 30*time.Second); err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		afterClaim := b1.Digest()
		if afterClaim == beforeClaim {
			t.Fatalf("digest did not change after claiming task lease")
		}

		// Snapshot must be fault-free even under 100% fault injection.
		b1.SetFaults(memstore.FaultConfig{
			GetErrProb:        1.0,
			QueryErrProb:      1.0,
			ApplyErrProb:      1.0,
			TxConflictProb:    1.0,
			CommitThenErrProb: 1.0,
		}, rand.New(rand.NewPCG(9, 10)))
		snap := b1.Snapshot()
		if b1.FaultStats().Total() != 0 {
			t.Fatalf("Snapshot triggered faults: %+v", b1.FaultStats())
		}
		if len(snap.TaskLeases) != 2 || len(snap.UserWakes) != 2 {
			t.Fatalf("unexpected snapshot lease/wake counts: leases=%d wakes=%d", len(snap.TaskLeases), len(snap.UserWakes))
		}
		snap.TaskLeases[0].Owner = "tampered"
		*snap.TaskLeases[0].LeaseUntil = fixed.Add(99 * time.Hour)
		snap.UserWakes[0].Lane = "tampered"
		if b1.Digest() != afterClaim {
			t.Fatalf("mutating snapshot altered live Backend state")
		}
	})
}

func runMemoryIndexConformanceSuite(t *testing.T, b docstore.Backend, idx docstore.MemoryIndex, prefix string) {
	t.Helper()
	ctx := context.Background()
	client := docstore.New(b)
	baseNow := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	t.Run("MemoryIndex_CRUDListAndTenantIsolation", func(t *testing.T) {
		ownerA := prefix + "_mi_a"
		ownerB := prefix + "_mi_b"

		chunks := []docstore.MemoryChunk{
			{
				Owner: ownerA, ChunkID: docstore.MemoryCoreChunkID("m2"), Layer: docstore.MemoryLayerCore,
				Section: "Projects", About: "the user", Body: "Building Nori memory index",
				Provenance: string(state.ProvenanceUserExplicit), CreatedAt: baseNow, UpdatedAt: baseNow,
			},
			{
				Owner: ownerA, ChunkID: docstore.MemoryCoreChunkID("m1"), Layer: docstore.MemoryLayerCore,
				Section: "Preferences", About: "the user", Body: "Prefers concise summaries",
				Provenance: string(state.ProvenanceUserExplicit), Pinned: true, CreatedAt: baseNow.Add(-time.Hour), UpdatedAt: baseNow.Add(-time.Hour),
			},
			{
				Owner: ownerA, ChunkID: docstore.MemoryLogChunkID("2026-11-23", "e1"), Layer: docstore.MemoryLayerLog,
				LogDate: "2026-11-23", Section: "Decisions", About: "the user", Body: "Chose 32 hash partitions",
				Provenance: string(state.ProvenancePreCompactFlush), CreatedAt: baseNow, UpdatedAt: baseNow,
			},
			{
				Owner: ownerB, ChunkID: docstore.MemoryCoreChunkID("other"), Layer: docstore.MemoryLayerCore,
				Section: "Secret", About: "other user", Body: "Building Nori memory index for tenant B",
				Provenance: string(state.ProvenanceUserExplicit), CreatedAt: baseNow, UpdatedAt: baseNow,
			},
		}
		for _, c := range chunks {
			if err := idx.UpsertMemoryChunk(ctx, c); err != nil {
				t.Fatalf("UpsertMemoryChunk(%s/%s): %v", c.Owner, c.ChunkID, err)
			}
		}

		// ListMemoryChunks returns sorted by chunk_id ASC and strictly isolated by owner.
		gotA, err := idx.ListMemoryChunks(ctx, ownerA)
		if err != nil {
			t.Fatalf("ListMemoryChunks(ownerA): %v", err)
		}
		if len(gotA) != 3 {
			t.Fatalf("ListMemoryChunks(ownerA) len = %d, want 3", len(gotA))
		}
		if gotA[0].ChunkID != "core:m1" || gotA[1].ChunkID != "core:m2" || gotA[2].ChunkID != "log:2026-11-23:e1" {
			t.Fatalf("unexpected chunk_id sort order: %q, %q, %q", gotA[0].ChunkID, gotA[1].ChunkID, gotA[2].ChunkID)
		}
		if gotA[2].LogDate != "2026-11-23" || !gotA[0].Pinned {
			t.Fatalf("chunk fields mismatch: %+v", gotA)
		}

		// Update existing chunk in place.
		updated := chunks[0]
		updated.Body = "Updated Nori FTS body"
		updated.UpdatedAt = baseNow.Add(time.Minute)
		if err := idx.UpsertMemoryChunk(ctx, updated); err != nil {
			t.Fatalf("UpsertMemoryChunk update: %v", err)
		}
		gotA, err = idx.ListMemoryChunks(ctx, ownerA)
		if err != nil || len(gotA) != 3 || gotA[1].Body != "Updated Nori FTS body" {
			t.Fatalf("update did not persist cleanly: got=%+v err=%v", gotA, err)
		}

		// Search isolates tenants strictly.
		hits, err := idx.SearchMemory(ctx, docstore.MemorySearchQuery{
			Owner: ownerA, Query: "tenant", Now: baseNow,
		})
		if err != nil {
			t.Fatalf("SearchMemory tenant isolation: %v", err)
		}
		if len(hits) != 0 {
			t.Fatalf("cross-tenant hit leaked into ownerA: %+v", hits)
		}

		// Delete chunk.
		if err := idx.DeleteMemoryChunk(ctx, ownerA, docstore.MemoryCoreChunkID("m2")); err != nil {
			t.Fatalf("DeleteMemoryChunk: %v", err)
		}
		gotA, err = idx.ListMemoryChunks(ctx, ownerA)
		if err != nil || len(gotA) != 2 {
			t.Fatalf("ListMemoryChunks after delete: len=%d err=%v", len(gotA), err)
		}
	})

	t.Run("MemoryIndex_DualDictionaryUUIDsAndCodeIdentifiers", func(t *testing.T) {
		owner := prefix + "_mi_dual"
		seed := []docstore.MemoryChunk{
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("stem"), Layer: docstore.MemoryLayerCore,
				Section: "Operations", About: "infra", Body: "We deployed canary releases and migrated schemas automatically",
				Provenance: string(state.ProvenanceUserExplicit), CreatedAt: baseNow, UpdatedAt: baseNow,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("uuid"), Layer: docstore.MemoryLayerCore,
				Section: "Incidents", About: "ops", Body: "Incident correlation ID 550e8400-e29b-41d4-a716-446655440000 in prod",
				Provenance: string(state.ProvenanceUserExplicit), CreatedAt: baseNow, UpdatedAt: baseNow,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("code"), Layer: docstore.MemoryLayerCore,
				Section: "Codebase", About: "backend", Body: "Handler flush_and_compact_handler returns ERR_STALE_42 for alice.smith@acme-corp.io",
				Provenance: string(state.ProvenanceUserExplicit), CreatedAt: baseNow, UpdatedAt: baseNow,
			},
		}
		for _, c := range seed {
			if err := idx.UpsertMemoryChunk(ctx, c); err != nil {
				t.Fatalf("seed %s: %v", c.ChunkID, err)
			}
		}

		cases := []struct {
			name   string
			query  string
			wantID string
		}{
			{"EnglishStemming", "deploying migrations", "core:stem"},
			{"FullUUID", "550e8400-e29b-41d4-a716-446655440000", "core:uuid"},
			{"UUIDSegment", "550e8400", "core:uuid"},
			{"SnakeCaseIdentifier", "flush_and_compact_handler", "core:code"},
			{"UpperCodeSymbol", "ERR_STALE_42", "core:code"},
			{"EmailIdentifier", "alice.smith@acme-corp.io", "core:code"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				hits, err := idx.SearchMemory(ctx, docstore.MemorySearchQuery{
					Owner: owner, Query: tc.query, Now: baseNow,
				})
				if err != nil {
					t.Fatalf("SearchMemory(%q): %v", tc.query, err)
				}
				if len(hits) == 0 || hits[0].Chunk.ChunkID != tc.wantID {
					t.Fatalf("SearchMemory(%q): got %+v, want top hit %q", tc.query, hits, tc.wantID)
				}
			})
		}
	})

	t.Run("MemoryIndex_RecencyDecayPinnedBoostLayerAndCreatedAfter", func(t *testing.T) {
		owner := prefix + "_mi_rank"
		day0 := baseNow
		day60Ago := baseNow.Add(-60 * 24 * time.Hour)
		day90Ago := baseNow.Add(-90 * 24 * time.Hour)

		seed := []docstore.MemoryChunk{
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("c_pinned"), Layer: docstore.MemoryLayerCore,
				Section: "Prefs", About: "user", Body: "quantum espresso calibration protocol",
				Provenance: string(state.ProvenanceUserExplicit), Pinned: true, CreatedAt: day90Ago, UpdatedAt: day90Ago,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("c_evergreen"), Layer: docstore.MemoryLayerCore,
				Section: "Prefs", About: "user", Body: "quantum espresso calibration protocol",
				Provenance: string(state.ProvenanceUserExplicit), Pinned: false, CreatedAt: day90Ago, UpdatedAt: day90Ago,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryLogChunkID("2026-11-23", "l_fresh"), Layer: docstore.MemoryLayerLog,
				LogDate: "2026-11-23", Section: "Notes", About: "user", Body: "quantum espresso calibration protocol",
				Provenance: string(state.ProvenancePreCompactFlush), CreatedAt: day0, UpdatedAt: day0,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryLogChunkID("2026-09-24", "l_stale"), Layer: docstore.MemoryLayerLog,
				LogDate: "2026-09-24", Section: "Notes", About: "user", Body: "quantum espresso calibration protocol",
				Provenance: string(state.ProvenancePreCompactFlush), CreatedAt: day60Ago, UpdatedAt: day60Ago,
			},
		}
		for _, c := range seed {
			if err := idx.UpsertMemoryChunk(ctx, c); err != nil {
				t.Fatalf("seed %s: %v", c.ChunkID, err)
			}
		}

		// Core layer: pinned (1.15) > unpinned (1.0).
		coreHits, err := idx.SearchMemory(ctx, docstore.MemorySearchQuery{
			Owner: owner, Query: "quantum espresso calibration", Layer: docstore.MemoryLayerCore, Now: day0,
		})
		if err != nil {
			t.Fatalf("SearchMemory core: %v", err)
		}
		if len(coreHits) != 2 || coreHits[0].Chunk.ChunkID != "core:c_pinned" || coreHits[1].Chunk.ChunkID != "core:c_evergreen" {
			t.Fatalf("unexpected core ranking: %+v", coreHits)
		}
		if coreHits[0].Score <= coreHits[1].Score {
			t.Fatalf("expected pinned score (%f) > unpinned score (%f)", coreHits[0].Score, coreHits[1].Score)
		}

		// Log layer: fresh (age=0d, factor=1.0) > stale (age=60d, factor=0.25).
		logHits, err := idx.SearchMemory(ctx, docstore.MemorySearchQuery{
			Owner: owner, Query: "quantum espresso calibration", Layer: docstore.MemoryLayerLog, Now: day0,
		})
		if err != nil {
			t.Fatalf("SearchMemory log: %v", err)
		}
		if len(logHits) != 2 || logHits[0].Chunk.ChunkID != "log:2026-11-23:l_fresh" || logHits[1].Chunk.ChunkID != "log:2026-09-24:l_stale" {
			t.Fatalf("unexpected log decay ranking: %+v", logHits)
		}
		ratio := logHits[1].Score / logHits[0].Score
		if ratio < 0.20 || ratio > 0.30 {
			t.Fatalf("60-day decay ratio = %f, want ~0.25 (30-day half-life)", ratio)
		}

		// CreatedAfter filter (INV-MEM-07): strictly >= CreatedAfter.
		cutoff := day60Ago.Add(time.Second)
		filtered, err := idx.SearchMemory(ctx, docstore.MemorySearchQuery{
			Owner: owner, Query: "quantum espresso", CreatedAfter: cutoff, Now: day0,
		})
		if err != nil {
			t.Fatalf("SearchMemory CreatedAfter: %v", err)
		}
		if len(filtered) != 1 || filtered[0].Chunk.ChunkID != "log:2026-11-23:l_fresh" {
			t.Fatalf("CreatedAfter filter mismatch: got %+v, want only l_fresh", filtered)
		}
	})

	t.Run("MemoryIndex_MMRDiversitySnippetCapAndTieBreak", func(t *testing.T) {
		owner := prefix + "_mi_mmr"
		longUTF8 := "nebula telescope calibration notes " + strings.Repeat("星雲観測データ ", 120)
		seed := []docstore.MemoryChunk{
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("a_dup1"), Layer: docstore.MemoryLayerCore,
				Section: "Astro", About: "nebula", Body: "nebula telescope mirror alignment steps alpha beta gamma delta",
				Provenance: string(state.ProvenanceUserExplicit), Pinned: true, CreatedAt: baseNow, UpdatedAt: baseNow,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("b_dup2"), Layer: docstore.MemoryLayerCore,
				Section: "Astro", About: "nebula", Body: "nebula telescope mirror alignment steps alpha beta gamma delta",
				Provenance: string(state.ProvenanceUserExplicit), Pinned: true, CreatedAt: baseNow, UpdatedAt: baseNow,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("c_diverse"), Layer: docstore.MemoryLayerCore,
				Section: "Schedule", About: "nebula", Body: "nebula observatory booking schedule winter mountain weather forecast",
				Provenance: string(state.ProvenanceUserExplicit), Pinned: true, CreatedAt: baseNow, UpdatedAt: baseNow,
			},
			{
				Owner: owner, ChunkID: docstore.MemoryCoreChunkID("d_long"), Layer: docstore.MemoryLayerCore,
				Section: "Notes", About: "nebula", Body: longUTF8,
				Provenance: string(state.ProvenanceUserExplicit), CreatedAt: baseNow, UpdatedAt: baseNow,
			},
		}
		for _, c := range seed {
			if err := idx.UpsertMemoryChunk(ctx, c); err != nil {
				t.Fatalf("seed %s: %v", c.ChunkID, err)
			}
		}

		hits, err := idx.SearchMemory(ctx, docstore.MemorySearchQuery{
			Owner: owner, Query: "nebula", Limit: 4, Now: baseNow,
		})
		if err != nil {
			t.Fatalf("SearchMemory MMR: %v", err)
		}
		if len(hits) != 4 {
			t.Fatalf("got %d hits, want 4", len(hits))
		}
		// Tie-break puts a_dup1 first, then MMR promotes diverse chunks ahead of exact duplicate b_dup2.
		if hits[0].Chunk.ChunkID != "core:a_dup1" {
			t.Fatalf("hits[0] = %q, want core:a_dup1 (chunk_id ASC tie-break)", hits[0].Chunk.ChunkID)
		}
		if hits[1].Chunk.ChunkID == "core:b_dup2" {
			t.Fatalf("MMR failed to demote exact duplicate core:b_dup2 behind diverse hits: order=%q, %q, %q, %q",
				hits[0].Chunk.ChunkID, hits[1].Chunk.ChunkID, hits[2].Chunk.ChunkID, hits[3].Chunk.ChunkID)
		}
		for _, h := range hits {
			if len(h.Snippet) > docstore.MaxMemorySnippetBytes {
				t.Fatalf("snippet for %s exceeded %d bytes: %d", h.Chunk.ChunkID, docstore.MaxMemorySnippetBytes, len(h.Snippet))
			}
			if !utf8.ValidString(h.Snippet) {
				t.Fatalf("snippet for %s split a multi-byte UTF-8 rune", h.Chunk.ChunkID)
			}
		}
	})

	t.Run("MemoryIndex_SameTransactionCommitAndRollback", func(t *testing.T) {
		owner := prefix + "_mi_tx"
		docRef := client.Collection("owners/" + owner + "/memories").Doc("tx1")
		chunk := docstore.MemoryChunk{
			Owner: owner, ChunkID: docstore.MemoryCoreChunkID("tx1"), Layer: docstore.MemoryLayerCore,
			Section: "Test", About: "tx", Body: "atomic rollback sentinel",
			Provenance: string(state.ProvenanceUserExplicit), CreatedAt: baseNow, UpdatedAt: baseNow,
		}

		// Aborted tx must roll back BOTH the document AND the FTS chunk.
		sentinelErr := errors.New("intentional rollback")
		err := client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.Set(docRef, map[string]any{"id": "tx1", "text": "atomic rollback sentinel"}); err != nil {
				return err
			}
			if err := tx.UpsertMemoryChunk(chunk); err != nil {
				return err
			}
			return sentinelErr
		})
		if !errors.Is(err, sentinelErr) {
			t.Fatalf("expected sentinelErr, got %v", err)
		}
		if _, err := docRef.Get(ctx); !errors.Is(err, docstore.ErrNotFound) {
			t.Fatalf("rolled-back doc leaked: %v", err)
		}
		if chunks, err := idx.ListMemoryChunks(ctx, owner); err != nil || len(chunks) != 0 {
			t.Fatalf("rolled-back memory chunk leaked: chunks=%+v err=%v", chunks, err)
		}

		// Committed tx persists BOTH atomically.
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.Set(docRef, map[string]any{"id": "tx1", "text": "atomic rollback sentinel"}); err != nil {
				return err
			}
			return tx.UpsertMemoryChunk(chunk)
		})
		if err != nil {
			t.Fatalf("committed tx failed: %v", err)
		}
		if chunks, err := idx.ListMemoryChunks(ctx, owner); err != nil || len(chunks) != 1 {
			t.Fatalf("committed chunk missing: chunks=%+v err=%v", chunks, err)
		}

		// Rolled-back delete leaves both intact.
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.Delete(docRef); err != nil {
				return err
			}
			if err := tx.DeleteMemoryChunk(owner, chunk.ChunkID); err != nil {
				return err
			}
			return sentinelErr
		})
		if !errors.Is(err, sentinelErr) {
			t.Fatalf("expected sentinelErr on delete rollback, got %v", err)
		}
		if chunks, err := idx.ListMemoryChunks(ctx, owner); err != nil || len(chunks) != 1 {
			t.Fatalf("rolled-back delete removed chunk: chunks=%+v err=%v", chunks, err)
		}

		// Committed delete removes both.
		err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			if err := tx.Delete(docRef); err != nil {
				return err
			}
			return tx.DeleteMemoryChunk(owner, chunk.ChunkID)
		})
		if err != nil {
			t.Fatalf("committed delete failed: %v", err)
		}
		if chunks, err := idx.ListMemoryChunks(ctx, owner); err != nil || len(chunks) != 0 {
			t.Fatalf("committed delete left chunk: chunks=%+v err=%v", chunks, err)
		}
	})
}

func runRecordsMemoryConformanceSuite(t *testing.T, b docstore.Backend, idx docstore.MemoryIndex, prefix string) {
	t.Helper()
	ctx := context.Background()

	newDB := func(suffix string, nowFn func() time.Time) (*records.Database, users.User) {
		u := users.User{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(prefix+"_"+suffix)).String()}
		db := records.NewShared(b, u, 1.0)
		db.Now = nowFn
		return db, u
	}

	assertBijection := func(t *testing.T, db *records.Database, owner string) {
		t.Helper()
		core, err := db.Memories(ctx)
		if err != nil {
			t.Fatalf("Memories: %v", err)
		}
		logs, err := db.MemoryLogs(ctx, "", 0)
		if err != nil {
			t.Fatalf("MemoryLogs: %v", err)
		}
		chunks, err := idx.ListMemoryChunks(ctx, owner)
		if err != nil {
			t.Fatalf("ListMemoryChunks: %v", err)
		}
		want := map[string]bool{}
		for _, m := range core {
			want[docstore.MemoryCoreChunkID(m.ID)] = true
		}
		for _, l := range logs {
			for _, e := range l.Entries {
				want[docstore.MemoryLogChunkID(l.Date, e.ID)] = true
			}
		}
		got := map[string]bool{}
		for _, c := range chunks {
			got[c.ChunkID] = true
		}
		if len(want) != len(got) {
			t.Fatalf("INV-MEM-06 bijection violated: store=%d chunks=%d (want=%v got=%v)", len(want), len(got), want, got)
		}
		for id := range want {
			if !got[id] {
				t.Fatalf("INV-MEM-06 missing chunk %q in MemoryIndex", id)
			}
		}
	}

	t.Run("Records_CommitFlushCompactDreamOverflowShardingAndCAS", func(t *testing.T) {
		now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
		db, u := newDB("flush", func() time.Time { return now })

		// 1. Reject ProvenanceTaskOutcome in CoreUpserts (INV-MEM-04).
		_, err := db.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			NextSummary: state.ConversationSummary{Through: now, FlushedThrough: now, UpdatedAt: now},
			CoreUpserts: []state.Memory{{ID: "bad_task", Text: "injected", Provenance: state.ProvenanceTaskOutcome}},
		})
		if !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("expected ErrInvalidResource for task_outcome Core upsert, got %v", err)
		}

		// 2. Commit initial Core memories (1 pinned + 14 unpinned x 1900B = 26.6 KiB > 24 KiB -> auto-demote oldest unpinned).
		upserts := []state.Memory{
			{
				ID: "m_pinned", Text: strings.Repeat("P", 1900), About: "the user", Section: "Preferences",
				Pinned: true, Provenance: state.ProvenanceUserExplicit, SourceInputID: "ui",
				CreatedAt: now.Add(-100 * time.Minute), UpdatedAt: now.Add(-100 * time.Minute),
			},
		}
		for i := 0; i < 14; i++ {
			ts := now.Add(time.Duration(i-50) * time.Minute)
			upserts = append(upserts, state.Memory{
				ID: fmt.Sprintf("m_%02d", i), Text: fmt.Sprintf("fact_%02d_", i) + strings.Repeat("x", 1890),
				About: "the user", Section: "Facts", Provenance: state.ProvenancePreCompactFlush,
				SourceInputID: "in1", CreatedAt: ts, UpdatedAt: ts,
			})
		}

		nextSum := state.ConversationSummary{
			FlushedThrough: now,
			Through:        now,
			Gist:           "Initial flushed gist",
			Items:          []state.SummaryItem{{ID: "si_1", Kind: "decision", Quote: "Use Postgres 18", InputID: "in1"}},
			Turns:          3,
			Model:          "test-model",
			UpdatedAt:      now,
		}
		savedSum, err := db.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			ExpectedSummary: state.ConversationSummary{},
			NextSummary:     nextSum,
			CoreUpserts:     upserts,
			DailyLogDate:    "2026-11-23",
			DailyLogEntries: []state.MemoryLogEntry{
				{ID: "log_1", Section: "Decisions", Text: "Deploy canary on Tuesday", Provenance: state.ProvenancePreCompactFlush, CreatedAt: now, UpdatedAt: now},
			},
		})
		if err != nil {
			t.Fatalf("CommitFlushAndCompact: %v", err)
		}

		// Core memory stays <= 24 KiB, pinned item survives at index 0, oldest unpinned items auto-demoted to 2026-11-23 log.
		core, err := db.Memories(ctx)
		if err != nil {
			t.Fatalf("Memories: %v", err)
		}
		if state.CoreMemorySize(core) > state.CoreMemoryBytes {
			t.Fatalf("CoreMemorySize = %d > %d", state.CoreMemorySize(core), state.CoreMemoryBytes)
		}
		if len(core) == 0 || core[0].ID != "m_pinned" || !core[0].Pinned {
			t.Fatalf("pinned Core memory was not preserved at index 0: %+v", core)
		}
		dayLog, err := db.MemoryLog(ctx, "2026-11-23")
		if err != nil {
			t.Fatalf("MemoryLog: %v", err)
		}
		if len(dayLog.Entries) < 3 {
			t.Fatalf("expected log_1 + auto-demoted Core entries in 2026-11-23 log, got %d", len(dayLog.Entries))
		}
		assertBijection(t, db, u.DataOwner())

		// 3. SaveConversationSummary CAS must reject FlushedThrough mismatch.
		staleExpected := savedSum
		staleExpected.FlushedThrough = now.Add(-time.Minute)
		if err := db.SaveConversationSummary(ctx, savedSum, staleExpected); !errors.Is(err, state.ErrConversationStale) {
			t.Fatalf("SaveConversationSummary with wrong FlushedThrough: got %v, want ErrConversationStale", err)
		}

		// 4. Daily Log sharding (> 128 entries) + deduplication + newest-date-first ordering.
		now = now.Add(time.Minute)
		manyEntries := make([]state.MemoryLogEntry, 0, 135)
		for i := 0; i < 135; i++ {
			manyEntries = append(manyEntries, state.MemoryLogEntry{
				ID: fmt.Sprintf("bulk_%03d", i), Section: "Context",
				Text: fmt.Sprintf("Bulk episodic entry %03d", i), Provenance: state.ProvenancePreCompactFlush,
				CreatedAt: now, UpdatedAt: now,
			})
		}
		// Include duplicate ID to verify idempotent dedup.
		manyEntries = append(manyEntries, manyEntries[0])
		dreamSum := savedSum
		dreamSum.DreamThrough = now
		dreamSum.LastDreamAt = now
		dreamSum.UpdatedAt = now
		if _, err := db.CommitDreamConsolidate(ctx, state.DreamConsolidateRequest{
			ExpectedSummary: savedSum,
			NextSummary:     dreamSum,
			DailyLogDate:    "2026-11-24",
			DailyLogEntries: manyEntries,
		}); err != nil {
			t.Fatalf("CommitDreamConsolidate sharding: %v", err)
		}

		merged24, err := db.MemoryLog(ctx, "2026-11-24")
		if err != nil {
			t.Fatalf("MemoryLog(2026-11-24): %v", err)
		}
		if len(merged24.Entries) != 135 {
			t.Fatalf("merged 2026-11-24 entries = %d, want 135 (deduped across 2 shards)", len(merged24.Entries))
		}
		allLogs, err := db.MemoryLogs(ctx, "", 0)
		if err != nil {
			t.Fatalf("MemoryLogs: %v", err)
		}
		if len(allLogs) != 2 || allLogs[0].Date != "2026-11-24" || allLogs[1].Date != "2026-11-23" {
			t.Fatalf("MemoryLogs newest-date-first order mismatch: %+v", allLogs)
		}
		assertBijection(t, db, u.DataOwner())
	})

	t.Run("Records_MutateMemoryAllKindsIdempotencyAndReceipts", func(t *testing.T) {
		now := time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)
		db, u := newDB("mutate", func() time.Time { return now })

		// Create Core + idempotent retry + conflict check.
		pinnedTrue := true
		createReq := state.MemoryMutationRequest{
			RequestID: "req-create-1",
			Kind:      state.MemoryMutateCreateCore,
			Text:      "Prefers dark mode in editor",
			About:     "the user",
			Section:   "Preferences",
			Pinned:    &pinnedTrue,
		}
		r1, err := db.MutateMemory(ctx, createReq)
		if err != nil {
			t.Fatalf("MutateMemory(create_core): %v", err)
		}
		// Persisted receipt matches.
		gotReceipt, err := db.Receipt(ctx, "req-create-1")
		if err != nil || gotReceipt.RequestID != r1.RequestID || !bytes.Equal(gotReceipt.Resource, r1.Resource) {
			t.Fatalf("Receipt mismatch: got=%+v err=%v", gotReceipt, err)
		}
		// Identical retry returns saved receipt even after clock moves.
		now = now.Add(time.Second)
		r1Retry, err := db.MutateMemory(ctx, createReq)
		if err != nil || !bytes.Equal(r1Retry.Resource, r1.Resource) {
			t.Fatalf("identical retry mismatch: got=%+v err=%v", r1Retry, err)
		}
		// Different payload with same RequestID returns ErrIdempotencyConflict.
		badRetry := createReq
		badRetry.Text = "Different payload"
		if _, err := db.MutateMemory(ctx, badRetry); !errors.Is(err, state.ErrIdempotencyConflict) {
			t.Fatalf("mismatched retry: got %v, want ErrIdempotencyConflict", err)
		}

		var createdRes state.MemoryMutationResult
		if err := json.Unmarshal(r1.Resource, &createdRes); err != nil || createdRes.Memory == nil {
			t.Fatalf("unmarshal create result: %v (%s)", err, r1.Resource)
		}
		memID := createdRes.Memory.ID
		rev := createdRes.Memory.UpdatedAt.UnixMilli()

		// Edit Core (stale revision rejected, current revision succeeds).
		if _, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-edit-stale", Kind: state.MemoryMutateEditCore, TargetID: memID,
			ExpectedRevision: rev - 1, Text: "Stale edit",
		}); !errors.Is(err, state.ErrRevisionConflict) {
			t.Fatalf("stale edit: got %v, want ErrRevisionConflict", err)
		}
		rEdit, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-edit-1", Kind: state.MemoryMutateEditCore, TargetID: memID,
			ExpectedRevision: rev, Text: "Prefers high-contrast dark mode",
		})
		if err != nil {
			t.Fatalf("edit_core: %v", err)
		}
		var editRes state.MemoryMutationResult
		_ = json.Unmarshal(rEdit.Resource, &editRes)
		rev = editRes.Memory.UpdatedAt.UnixMilli()

		// Unpin -> Pin -> Demote Core to Daily Log.
		now = now.Add(time.Second)
		rUnpin, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-unpin-1", Kind: state.MemoryMutateUnpinCore, TargetID: memID, ExpectedRevision: rev,
		})
		if err != nil {
			t.Fatalf("unpin_core: %v", err)
		}
		var unpinRes state.MemoryMutationResult
		_ = json.Unmarshal(rUnpin.Resource, &unpinRes)
		if unpinRes.Memory.Pinned {
			t.Fatalf("expected Pinned=false after unpin_core")
		}
		rev = unpinRes.Memory.UpdatedAt.UnixMilli()

		now = now.Add(time.Second)
		rPin, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-pin-1", Kind: state.MemoryMutatePinCore, TargetID: memID, ExpectedRevision: rev,
		})
		if err != nil {
			t.Fatalf("pin_core: %v", err)
		}
		var pinRes state.MemoryMutationResult
		_ = json.Unmarshal(rPin.Resource, &pinRes)
		if !pinRes.Memory.Pinned {
			t.Fatalf("expected Pinned=true after pin_core")
		}
		rev = pinRes.Memory.UpdatedAt.UnixMilli()

		now = now.Add(time.Second)
		rDemote, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-demote-1", Kind: state.MemoryMutateDemoteCore, TargetID: memID, ExpectedRevision: rev, Date: "2026-11-23",
		})
		if err != nil {
			t.Fatalf("demote_core: %v", err)
		}
		var demoteRes state.MemoryMutationResult
		_ = json.Unmarshal(rDemote.Resource, &demoteRes)
		if demoteRes.LogEntry == nil {
			t.Fatalf("expected LogEntry from demote_core")
		}
		assertBijection(t, db, u.DataOwner())

		// Seed a Working Memory summary and a task_outcome Daily Log entry.
		now = now.Add(time.Second)
		sum := state.ConversationSummary{
			FlushedThrough: now, Through: now, Gist: "Working gist",
			Items:     []state.SummaryItem{{ID: "si_work1", Kind: "preference", Quote: "Keep builds under 10s", InputID: "in_user_1"}},
			UpdatedAt: now,
		}
		if _, err := db.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			NextSummary:  sum,
			DailyLogDate: "2026-11-23",
			DailyLogEntries: []state.MemoryLogEntry{
				{ID: "task_r99", Section: "Completed Work", Text: "Ran benchmarks: 99ms p95", Provenance: state.ProvenanceTaskOutcome, RunID: "r99", CreatedAt: now, UpdatedAt: now},
			},
		}); err != nil {
			t.Fatalf("seed working+task log: %v", err)
		}

		// Promote task_outcome log entry WITHOUT explicit Text must fail (INV-MEM-04).
		dayLog, _ := db.MemoryLog(ctx, "2026-11-23")
		logRev := dayLog.UpdatedAt.UnixMilli()
		if _, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-promote-task-fail", Kind: state.MemoryMutatePromoteLog,
			TargetID: "task_r99", Date: "2026-11-23", ExpectedRevision: logRev,
		}); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("promote task_outcome without Text: got %v, want ErrInvalidResource", err)
		}
		// Promote task_outcome WITH explicit re-authored Text succeeds as ProvenanceUserExplicit / SourceInputID="ui".
		now = now.Add(time.Second)
		rPromLog, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-promote-task-ok", Kind: state.MemoryMutatePromoteLog,
			TargetID: "task_r99", Date: "2026-11-23", ExpectedRevision: logRev,
			Text: "Our p95 latency target is under 100ms",
		})
		if err != nil {
			t.Fatalf("promote task_outcome with explicit text: %v", err)
		}
		var promLogRes state.MemoryMutationResult
		_ = json.Unmarshal(rPromLog.Resource, &promLogRes)
		if promLogRes.Memory.Provenance != state.ProvenanceUserExplicit || promLogRes.Memory.SourceInputID != "ui" {
			t.Fatalf("re-authored promoted memory provenance mismatch: %+v", promLogRes.Memory)
		}

		// Forget log entry removes it and its FTS chunk.
		now = now.Add(time.Second)
		if _, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-forget-log-1", Kind: state.MemoryMutateForgetLog,
			TargetID: "task_r99", Date: "2026-11-23", ExpectedRevision: logRev,
		}); err != nil {
			t.Fatalf("forget_log: %v", err)
		}

		// Promote Working Memory item -> Dismiss Working Memory item.
		sumRev := sum.UpdatedAt.UnixMilli()
		now = now.Add(time.Second)
		rPromWork, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-promote-work-1", Kind: state.MemoryMutatePromoteWorking,
			TargetID: "si_work1", ExpectedRevision: sumRev,
		})
		if err != nil {
			t.Fatalf("promote_working: %v", err)
		}
		var promWorkRes state.MemoryMutationResult
		_ = json.Unmarshal(rPromWork.Resource, &promWorkRes)
		if promWorkRes.Memory.SourceInputID != "in_user_1" {
			t.Fatalf("promote_working SourceInputID = %q, want in_user_1", promWorkRes.Memory.SourceInputID)
		}

		now = now.Add(time.Second)
		rDismiss, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-dismiss-work-1", Kind: state.MemoryMutateDismissWorking,
			TargetID: "si_work1", ExpectedRevision: sumRev,
		})
		if err != nil {
			t.Fatalf("dismiss_working: %v", err)
		}
		var dismissRes state.MemoryMutationResult
		_ = json.Unmarshal(rDismiss.Resource, &dismissRes)
		if len(dismissRes.Summary.Items) != 0 || !slices.Contains(dismissRes.Summary.DismissedItemIDs, "si_work1") {
			t.Fatalf("dismiss_working summary mismatch: %+v", dismissRes.Summary)
		}

		// Forget Core memory.
		now = now.Add(time.Second)
		if _, err := db.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-forget-core-1", Kind: state.MemoryMutateForgetCore,
			TargetID: promWorkRes.Memory.ID, ExpectedRevision: promWorkRes.Memory.UpdatedAt.UnixMilli(),
		}); err != nil {
			t.Fatalf("forget_core: %v", err)
		}

		// SaveMemoryActionReceipt idempotency and conflict check.
		rcpt, err := db.SaveMemoryActionReceipt(ctx, "req-consolidate-1", "bind:consolidate", json.RawMessage(`{"ok":true}`))
		if err != nil || !rcpt.Accepted {
			t.Fatalf("SaveMemoryActionReceipt: %+v err=%v", rcpt, err)
		}
		rcpt2, err := db.SaveMemoryActionReceipt(ctx, "req-consolidate-1", "bind:consolidate", json.RawMessage(`{"ok":true}`))
		if err != nil || !bytes.Equal(rcpt2.Resource, rcpt.Resource) {
			t.Fatalf("SaveMemoryActionReceipt retry: %+v err=%v", rcpt2, err)
		}
		if _, err := db.SaveMemoryActionReceipt(ctx, "req-consolidate-1", "bind:other", json.RawMessage(`{"ok":false}`)); !errors.Is(err, state.ErrIdempotencyConflict) {
			t.Fatalf("SaveMemoryActionReceipt conflict: got %v, want ErrIdempotencyConflict", err)
		}

		assertBijection(t, db, u.DataOwner())
	})

	t.Run("Records_TaskPublishAppendsTaskOutcomeLogOnly", func(t *testing.T) {
		now := time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)
		db, u := newDB("taskpub", func() time.Time { return now })

		receipts, err := db.AcceptCommandBatch(ctx, []state.Command{{
			ID:        "cmd-pub-1",
			RunID:     "run-pub-1",
			Kind:      state.CommandStart,
			Text:      "Analyze quarterly revenue trends",
			CreatedAt: now,
		}}, nil)
		if err != nil || len(receipts) != 1 {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		run := receipts[0].Run
		if err := db.Incorporate(ctx, state.IncorporateRequest{RunID: run.ID, Revision: run.Revision, Position: run.Position}); err != nil {
			t.Fatalf("Incorporate: %v", err)
		}

		longOutcome := "Q4 revenue rose 18% YoY driven by enterprise expansion. " + strings.Repeat("details ", 120)
		finished, err := db.Publish(ctx, state.PublishRequest{
			RunID:         run.ID,
			Revision:      run.Revision,
			Status:        state.RunCompleted,
			Text:          longOutcome,
			BypassRouting: true,
		})
		if err != nil || finished.Status != state.RunCompleted {
			t.Fatalf("Publish: status=%v err=%v", finished.Status, err)
		}

		// Core memory MUST remain untouched (INV-MEM-04).
		core, err := db.Memories(ctx)
		if err != nil || len(core) != 0 {
			t.Fatalf("task completion mutated CoreMemory: %+v err=%v", core, err)
		}

		// Daily Log MUST contain bounded (<= 600B) task_outcome entry indexed in MemoryIndex.
		dayLog, err := db.MemoryLog(ctx, "2026-11-23")
		if err != nil {
			t.Fatalf("MemoryLog after task Publish: %v", err)
		}
		if len(dayLog.Entries) != 1 {
			t.Fatalf("expected 1 task_outcome log entry, got %d", len(dayLog.Entries))
		}
		e := dayLog.Entries[0]
		if e.Provenance != state.ProvenanceTaskOutcome || e.RunID != run.ID || e.Section != "Completed Work" {
			t.Fatalf("unexpected task_outcome entry: %+v", e)
		}
		if len(e.Text) == 0 || len(e.Text) > state.MaxTaskOutcomeBytes || !utf8.ValidString(e.Text) {
			t.Fatalf("task_outcome text exceeded bounds: len=%d valid=%v", len(e.Text), utf8.ValidString(e.Text))
		}
		hits, err := db.SearchMemory(ctx, docstore.MemorySearchQuery{Query: "quarterly revenue enterprise", Now: now})
		if err != nil || len(hits) != 1 || hits[0].Chunk.RunID != run.ID {
			t.Fatalf("SearchMemory did not find task_outcome chunk: hits=%+v err=%v", hits, err)
		}
		assertBijection(t, db, u.DataOwner())
	})

	t.Run("Records_PublishConversationRememberForgetAndOverflow", func(t *testing.T) {
		now := time.Date(2026, 11, 23, 21, 0, 0, 0, time.UTC)
		db, u := newDB("pubconv", func() time.Time { return now })

		if err := db.AcceptConversationInput(ctx, "in-mem-1", "Remember I love espresso", nil); err != nil {
			t.Fatalf("AcceptConversationInput: %v", err)
		}
		ctrl, turn, err := db.AcquireConversation(ctx, "w1", "turn-mem-1")
		if err != nil || turn == nil {
			t.Fatalf("AcquireConversation: turn=%v err=%v", turn, err)
		}
		turn.Attempt.ID = "att-mem-1"
		turn.Attempt.Memory = []state.MemoryChange{
			{Kind: state.MemoryRemember, ID: "conv_m1", Text: "User loves double espresso in the morning", About: "the user", Section: "Preferences"},
		}
		fence := state.ConversationFence{Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID, Through: turn.Through, UserThrough: turn.UserThrough}
		if err := db.PublishConversation(ctx, fence, *turn, "Noted!"); err != nil {
			t.Fatalf("PublishConversation(remember): %v", err)
		}

		// Verify Core memory + FTS chunk + EventMemory.
		mems, err := db.Memories(ctx)
		if err != nil || len(mems) != 1 || mems[0].ID != "conv_m1" || mems[0].SourceInputID != "in-mem-1" {
			t.Fatalf("Memories after PublishConversation: %+v err=%v", mems, err)
		}
		hits, err := db.SearchMemory(ctx, docstore.MemorySearchQuery{Query: "double espresso", Now: now})
		if err != nil || len(hits) != 1 || hits[0].Chunk.ChunkID != "core:conv_m1" {
			t.Fatalf("SearchMemory after PublishConversation: hits=%+v err=%v", hits, err)
		}
		assertBijection(t, db, u.DataOwner())

		// Forget via PublishConversation removes Core memory and FTS chunk.
		now = now.Add(time.Minute)
		if err := db.AcceptConversationInput(ctx, "in-mem-2", "Forget espresso", nil); err != nil {
			t.Fatalf("AcceptConversationInput(2): %v", err)
		}
		ctrl2, turn2, err := db.AcquireConversation(ctx, "w1", "turn-mem-2")
		if err != nil || turn2 == nil {
			t.Fatalf("AcquireConversation(2): turn=%v err=%v", turn2, err)
		}
		turn2.Attempt.ID = "att-mem-2"
		turn2.Attempt.Memory = []state.MemoryChange{
			{Kind: state.MemoryForget, ID: "conv_m1"},
		}
		fence2 := state.ConversationFence{Owner: ctrl2.Owner, Fence: ctrl2.Fence, TurnID: turn2.ID, Through: turn2.Through, UserThrough: turn2.UserThrough}
		if err := db.PublishConversation(ctx, fence2, *turn2, "Forgotten."); err != nil {
			t.Fatalf("PublishConversation(forget): %v", err)
		}
		mems, _ = db.Memories(ctx)
		if len(mems) != 0 {
			t.Fatalf("expected 0 memories after forget, got %+v", mems)
		}
		assertBijection(t, db, u.DataOwner())
	})
}

func TestMemstoreMemoryIndex_SnapshotRestoreDigestAndConflicts(t *testing.T) {
	ctx := context.Background()
	fixed := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return fixed }

	b1 := memstore.New(memstore.WithClock(clock))
	b2 := memstore.New(memstore.WithClock(clock))

	c1 := docstore.MemoryChunk{
		Owner: "u1", ChunkID: "core:m1", Layer: docstore.MemoryLayerCore,
		Section: "Prefs", About: "user", Body: "Alpha memory", Provenance: string(state.ProvenanceUserExplicit),
		Pinned: true, CreatedAt: fixed, UpdatedAt: fixed,
	}
	c2 := docstore.MemoryChunk{
		Owner: "u1", ChunkID: "log:2026-11-23:e1", Layer: docstore.MemoryLayerLog, LogDate: "2026-11-23",
		Section: "Notes", About: "user", Body: "Beta episodic note", Provenance: string(state.ProvenancePreCompactFlush),
		CreatedAt: fixed, UpdatedAt: fixed,
	}

	// Insertion order independence in Digest().
	_ = b1.UpsertMemoryChunk(ctx, c1)
	_ = b1.UpsertMemoryChunk(ctx, c2)
	_ = b2.UpsertMemoryChunk(ctx, c2)
	_ = b2.UpsertMemoryChunk(ctx, c1)
	if b1.Digest() != b2.Digest() {
		t.Fatalf("digests diverged across chunk insertion order: %s vs %s", b1.Digest(), b2.Digest())
	}

	// Snapshot includes MemoryChunks and is fault-free + isolated from live Backend.
	b1.SetFaults(memstore.FaultConfig{
		GetErrProb: 1.0, QueryErrProb: 1.0, ApplyErrProb: 1.0, TxConflictProb: 1.0, CommitThenErrProb: 1.0,
	}, rand.New(rand.NewPCG(11, 12)))
	snap := b1.Snapshot()
	if b1.FaultStats().Total() != 0 {
		t.Fatalf("Snapshot triggered faults: %+v", b1.FaultStats())
	}
	if len(snap.MemoryChunks) != 2 {
		t.Fatalf("Snapshot.MemoryChunks len = %d, want 2", len(snap.MemoryChunks))
	}
	origDigest := b1.Digest()
	snap.MemoryChunks[0].Body = "tampered"
	if b1.Digest() != origDigest {
		t.Fatalf("mutating snapshot altered live Backend digest")
	}

	// Restore parity onto a fresh Backend.
	b1.SetFaultsEnabled(false)
	freshSnap := b1.Snapshot()
	b3 := memstore.New(memstore.WithClock(clock))
	b3.Restore(freshSnap)
	if b3.Digest() != origDigest {
		t.Fatalf("Restore digest mismatch: got %s, want %s", b3.Digest(), origDigest)
	}
	chunks, err := b3.ListMemoryChunks(ctx, "u1")
	if err != nil || len(chunks) != 2 {
		t.Fatalf("ListMemoryChunks after Restore: chunks=%+v err=%v", chunks, err)
	}

	// Forced ErrTxConflict rolls back staged chunkWrites.
	client := docstore.New(b3)
	b3.SetFaults(memstore.FaultConfig{TxConflictProb: 1.0}, rand.New(rand.NewPCG(13, 14)))
	err = client.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		return tx.UpsertMemoryChunk(docstore.MemoryChunk{
			Owner: "u1", ChunkID: "core:conflict", Layer: docstore.MemoryLayerCore,
			Body: "should not commit", Provenance: string(state.ProvenanceUserExplicit), CreatedAt: fixed, UpdatedAt: fixed,
		})
	}, docstore.MaxAttempts(2))
	if !errors.Is(err, memstore.ErrTxConflict) {
		t.Fatalf("expected ErrTxConflict, got %v", err)
	}
	b3.SetFaultsEnabled(false)
	if chunks, _ := b3.ListMemoryChunks(ctx, "u1"); len(chunks) != 2 {
		t.Fatalf("conflicted tx leaked memory chunk: %+v", chunks)
	}
}
