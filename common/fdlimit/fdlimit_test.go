package fdlimit

import (
	"fmt"
	"testing"
)

func TestFileDescriptorLimits(t *testing.T) {
	target := 4096
	hardlimit, err := Maximum()
	if err != nil {
		t.Fatal(err)
	}
	if hardlimit < target {
		t.Skip(fmt.Sprintf("system limit is less than desired test target: %d < %d", hardlimit, target))
	}

	if limit, err := Current(); err != nil || limit <= 0 {
		t.Fatalf("failed to retrieve file descriptor limit (%d): %v", limit, err)
	}
	if err := Raise(uint64(target)); err != nil {
		t.Fatalf("failed to raise file allowance")
	}
	if limit, err := Current(); err != nil || limit < target {
		t.Fatalf("failed to retrieve raised descriptor limit (have %v, want %v): %v", limit, target, err)
	}
}
