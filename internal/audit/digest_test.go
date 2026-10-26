package audit

import (
	"strings"
	"testing"
)

func TestCanonicalJSONOrdersMapKeys(t *testing.T) {
	a, err := CanonicalJSON(map[string]any{"z": 1, "a": 2, "m": 3})
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	b, err := CanonicalJSON(map[string]any{"m": 3, "z": 1, "a": 2})
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("map key order not canonical: %s vs %s", a, b)
	}
	if !strings.HasPrefix(string(a), `{"a":`) {
		t.Fatalf("keys not sorted: %s", a)
	}
}

func TestCanonicalJSONPrimitives(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{value: nil, want: "null"},
		{value: true, want: "true"},
		{value: "text", want: `"text"`},
		{value: 42, want: "42"},
	}
	for _, testCase := range cases {
		raw, err := CanonicalJSON(testCase.value)
		if err != nil {
			t.Fatalf("CanonicalJSON(%v) error = %v", testCase.value, err)
		}
		if string(raw) != testCase.want {
			t.Fatalf("CanonicalJSON(%v) = %s, want %s", testCase.value, raw, testCase.want)
		}
	}
}

func TestCanonicalJSONRejectsUnmarshalable(t *testing.T) {
	if _, err := CanonicalJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("CanonicalJSON(chan) accepted an unmarshalable value")
	}
}

func TestCanonicalDigestStable(t *testing.T) {
	value := map[string]any{"tool": "search", "query": "test"}
	digest1, size1, err := CanonicalDigest(value)
	if err != nil {
		t.Fatalf("CanonicalDigest() error = %v", err)
	}
	digest2, size2, err := CanonicalDigest(map[string]any{"query": "test", "tool": "search"})
	if err != nil {
		t.Fatalf("CanonicalDigest() error = %v", err)
	}
	if digest1 != digest2 {
		t.Fatalf("digest not deterministic: %s vs %s", digest1, digest2)
	}
	if size1 != size2 || size1 <= 0 {
		t.Fatalf("sizes %d/%d, want equal and positive", size1, size2)
	}
	if !strings.HasPrefix(digest1, "sha256:") {
		t.Fatalf("digest = %q, want sha256: prefix", digest1)
	}
}

func TestCanonicalDigestRejectsUnmarshalable(t *testing.T) {
	_, _, err := CanonicalDigest(make(chan int))
	if err == nil {
		t.Fatal("CanonicalDigest(chan) accepted an unmarshalable value")
	}
}
