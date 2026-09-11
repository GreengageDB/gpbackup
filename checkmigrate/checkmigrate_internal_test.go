package checkmigrate

import (
	"strings"
	"sync"
	"testing"

	"github.com/GreengageDB/gp-common-go-libs/gplog"
)

func TestIncompatibleRangePartitionQueryFiltersPartitionKindAndOpenBounds(t *testing.T) {
	if !strings.Contains(incompatibleRangePartitionQuery, "p.parkind = 'r'") {
		t.Fatal("The incompatible range partition query must exclude non-range partitions")
	}

	for _, boundary := range []string{"parrangestart", "parrangeend"} {
		if !strings.Contains(incompatibleRangePartitionQuery, boundary+" <> '<>'") {
			t.Fatalf("The incompatible range partition query must exclude an open %s boundary", boundary)
		}
	}
}

func TestDoCleanupRunsOnce(t *testing.T) {
	gplog.InitializeLogging("ggcheckmigrate-test", t.TempDir())
	CleanupGroup = &sync.WaitGroup{}
	CleanupGroup.Add(1)
	cleanupOnce = sync.Once{}
	bootstrapSourceConnection = nil
	targetConnection = nil

	var callers sync.WaitGroup
	callers.Add(2)
	for callerIndex := 0; callerIndex < 2; callerIndex++ {
		go func() {
			defer callers.Done()
			DoCleanup(false)
		}()
	}

	callers.Wait()
	CleanupGroup.Wait()
}
