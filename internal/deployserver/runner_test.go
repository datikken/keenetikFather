package deployserver

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestRunRejectsNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux is the supported deployment platform")
	}

	err := Run(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires Linux") {
		t.Fatalf("Run() error = %v, want Linux platform error", err)
	}
}
