package domain_test

import (
	"strings"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
)

func TestIsValidSHA256Digest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  bool
	}{
		{value: "sha256:" + strings.Repeat("a", 64), want: true},
		{value: "sha256:" + strings.Repeat("A", 64), want: false},
		{value: "sha256:abc", want: false},
		{value: "", want: false},
	}
	for _, test := range tests {
		if got := domain.IsValidSHA256Digest(test.value); got != test.want {
			t.Fatalf("IsValidSHA256Digest(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}
