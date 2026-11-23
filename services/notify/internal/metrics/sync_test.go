package metrics

import (
	"bytes"
	"os"
	"testing"
)

// The exposition library is kept as a byte-identical copy in services/kb (see the
// package comment). This fails when one copy is edited without the other.
func TestCopyMatchesKbCopy(t *testing.T) {
	for _, f := range []string{"metrics.go", "metrics_test.go"} {
		other, err := os.ReadFile("../../../kb/internal/metrics/" + f)
		if err != nil {
			t.Skipf("services/kb is not next to this module: %v", err)
		}
		mine, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(mine, other) {
			t.Errorf("%s differs from services/kb/internal/metrics/%s: copy the edited file over the other", f, f)
		}
	}
}
