package executor

import (
	"strings"
	"testing"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestLookupEnv(t *testing.T) {
	svc := admiral.ServiceInfo{
		Env:     map[string]string{"K1": "V1"},
		Secrets: map[string]string{"S1": "V2"},
	}
	if v, ok := lookupEnv(svc, "K1"); !ok || v != "V1" {
		t.Errorf("lookupEnv K1 = %v, %v", v, ok)
	}
	if v, ok := lookupEnv(svc, "S1"); !ok || v != "V2" {
		t.Errorf("lookupEnv S1 = %v, %v", v, ok)
	}
	if _, ok := lookupEnv(svc, "NONE"); ok {
		t.Error("lookupEnv NONE should fail")
	}
}

func TestFindService(t *testing.T) {
	services := []admiral.ServiceInfo{{Name: "s1"}, {Name: "s2"}}
	if s := findService(services, "s1"); s.Name != "s1" {
		t.Error("findService s1 failed")
	}
	if s := findService(services, "none"); s.Name != "" {
		t.Error("findService none failed")
	}
}

func TestResizeMetadata(t *testing.T) {
	tier := admiral.TierInfo{Name: "t1", CPU: 1}
	meta := resizeMetadata(tier)
	if !strings.Contains(meta, "t1") {
		t.Errorf("resizeMetadata: %s", meta)
	}
}

func TestHasValidTaskMemoryLimit(t *testing.T) {
	tests := []struct {
		val  string
		want bool
	}{
		{"512MiB", true},
		{"1g", true},
		{"invalid", false},
		{"", false},
		{"512", false},
	}
	for _, tt := range tests {
		if got := hasValidTaskMemoryLimit(tt.val); got != tt.want {
			t.Errorf("hasValidTaskMemoryLimit(%q) = %v", tt.val, got)
		}
	}
}

func TestParsePublishedPort(t *testing.T) {
	tests := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{"80", 80, false},
		{"", 0, true},
		{"invalid", 0, true},
	}
	for _, tt := range tests {
		got, err := parsePublishedPort(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePublishedPort(%q) error = %v", tt.input, err)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parsePublishedPort(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
