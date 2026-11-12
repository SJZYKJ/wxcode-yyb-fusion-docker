package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureSettingKeepsFirstValue(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "yyb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const workers = 8
	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = db.EnsureSetting(ctx, "device_seed", fmt.Sprintf("candidate-%d", i))
		}(i)
	}
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("EnsureSetting() worker %d error = %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("concurrent writers disagree: %q vs %q", results[i], results[0])
		}
	}
	if again, err := db.EnsureSetting(ctx, "device_seed", "late"); err != nil || again != results[0] {
		t.Fatalf("EnsureSetting() after first write = %q, %v; want %q", again, err, results[0])
	}
}
