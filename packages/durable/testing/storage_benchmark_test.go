package durabletesting_test

import (
	"context"
	"testing"

	"github.com/minifish-org/pith/packages/durable/storage/memory"
	durabletesting "github.com/minifish-org/pith/packages/durable/testing"
)

func TestStorageBenchmarkPrimaryRecordCount(t *testing.T) {
	if got := durabletesting.StorageBenchmarkPrimaryRecordCount(durabletesting.TimingScale); got != 1870 {
		t.Fatalf("timing primary record count = %d want 1870", got)
	}
	if got := durabletesting.StorageBenchmarkPrimaryRecordCount(durabletesting.StorageMemoryScales[1]); got != 1+10_000+2_000+2_000+4+1+8*33 {
		t.Fatalf("10k primary record count = %d", got)
	}
}

func TestStorageBenchmarkScales(t *testing.T) {
	if len(durabletesting.StorageMemoryScales) != 2 {
		t.Fatalf("memory scales = %d", len(durabletesting.StorageMemoryScales))
	}
	if durabletesting.StorageMemoryScales[0].Name != "1k" || durabletesting.StorageMemoryScales[1].Name != "10k" {
		t.Fatalf("memory scale names = %+v", durabletesting.StorageMemoryScales)
	}
}

// TestStorageReadBenchmarksMemory seeds the representative dataset and checks
// that every read benchmark produces its declared expected value.
func TestStorageReadBenchmarksMemory(t *testing.T) {
	ctx := context.Background()
	storage := memory.New()
	defer func() { _ = storage.Close(ctx) }()
	dataset, err := durabletesting.SeedStorageBenchmark(ctx, storage, durabletesting.TimingScale)
	if err != nil {
		t.Fatal(err)
	}
	if len(durabletesting.StorageReadBenchmarks) == 0 {
		t.Fatal("no read benchmarks")
	}
	for _, benchmark := range durabletesting.StorageReadBenchmarks {
		got, err := benchmark.Run(ctx, storage, dataset)
		if err != nil {
			t.Fatalf("%s: %v", benchmark.Name, err)
		}
		if want := benchmark.Expected(dataset); got != want {
			t.Fatalf("%s = %d want %d", benchmark.Name, got, want)
		}
	}
}

// TestStorageWriteBenchmarksMemory runs each write benchmark on an isolated
// adapter and checks its declared count.
func TestStorageWriteBenchmarksMemory(t *testing.T) {
	ctx := context.Background()
	for _, benchmark := range durabletesting.StorageWriteBenchmarks {
		benchmark := benchmark
		t.Run(benchmark.Name, func(t *testing.T) {
			storage := memory.New()
			defer func() { _ = storage.Close(ctx) }()
			if err := durabletesting.SeedStorageWriteBenchmark(ctx, storage); err != nil {
				t.Fatal(err)
			}
			got, err := benchmark.Run(ctx, storage)
			if err != nil {
				t.Fatal(err)
			}
			if got != benchmark.Expected {
				t.Fatalf("%s = %d want %d", benchmark.Name, got, benchmark.Expected)
			}
		})
	}
}

// BenchmarkStorageReadMemory is an optional developer benchmark; it does not run
// automatically and is not a performance guarantee.
func BenchmarkStorageReadMemory(b *testing.B) {
	ctx := context.Background()
	storage := memory.New()
	defer func() { _ = storage.Close(ctx) }()
	dataset, err := durabletesting.SeedStorageBenchmark(ctx, storage, durabletesting.TimingScale)
	if err != nil {
		b.Fatal(err)
	}
	for _, benchmark := range durabletesting.StorageReadBenchmarks {
		benchmark := benchmark
		b.Run(benchmark.Name, func(b *testing.B) {
			for index := 0; index < b.N; index++ {
				if _, err := benchmark.Run(ctx, storage, dataset); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
