package config

import (
	"slices"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func required(extra map[string]string) map[string]string {
	values := map[string]string{"JWT_SECRET": "jwt", "TIME_TOKEN_SECRET": "time"}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(required(nil)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %q, want %q", cfg.Port, DefaultPort)
	}
	if cfg.TimeWindow != DefaultTimeWindowSeconds {
		t.Errorf("TimeWindow = %d, want %d", cfg.TimeWindow, DefaultTimeWindowSeconds)
	}
	if cfg.AllowedSkew != DefaultAllowedClockSkew {
		t.Errorf("AllowedSkew = %d, want %d", cfg.AllowedSkew, DefaultAllowedClockSkew)
	}
	if cfg.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.MaxBodyBytes, DefaultMaxBodyBytes)
	}
	if cfg.AllowedOrigins != nil {
		t.Errorf("AllowedOrigins = %v, want nil", cfg.AllowedOrigins)
	}
	if cfg.PprofAddr != "" {
		t.Errorf("PprofAddr = %q, want empty", cfg.PprofAddr)
	}
	if cfg.JWTSecret != "jwt" || cfg.TimeTokenSecret != "time" {
		t.Errorf("secrets not loaded: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(required(map[string]string{
		"PORT":                "9090",
		"TIME_WINDOW_SECONDS": "30",
		"ALLOWED_CLOCK_SKEW":  "0",
		"MAX_BODY_BYTES":      "2048",
		"ALLOWED_ORIGINS":     "https://a.example, https://b.example ,,",
		"PPROF_ADDR":          "127.0.0.1:6060",
	})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "9090" || cfg.TimeWindow != 30 || cfg.AllowedSkew != 0 || cfg.MaxBodyBytes != 2048 {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if want := []string{"https://a.example", "https://b.example"}; !slices.Equal(cfg.AllowedOrigins, want) {
		t.Errorf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
	}
	if cfg.PprofAddr != "127.0.0.1:6060" {
		t.Errorf("PprofAddr = %q", cfg.PprofAddr)
	}
}

func TestLoadTrimsIntegerWhitespace(t *testing.T) {
	cfg, err := Load(env(required(map[string]string{"TIME_WINDOW_SECONDS": " 45 "})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TimeWindow != 45 {
		t.Errorf("TimeWindow = %d, want 45", cfg.TimeWindow)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantErr []string
	}{
		{"missing jwt secret", map[string]string{"TIME_TOKEN_SECRET": "x"}, []string{"JWT_SECRET is required"}},
		{"missing time secret", map[string]string{"JWT_SECRET": "x"}, []string{"TIME_TOKEN_SECRET is required"}},
		{"missing both secrets", map[string]string{}, []string{"JWT_SECRET is required", "TIME_TOKEN_SECRET is required"}},
		{"zero window", required(map[string]string{"TIME_WINDOW_SECONDS": "0"}), []string{"TIME_WINDOW_SECONDS must be >= 1"}},
		{"negative window", required(map[string]string{"TIME_WINDOW_SECONDS": "-5"}), []string{"TIME_WINDOW_SECONDS must be >= 1"}},
		{"non numeric window", required(map[string]string{"TIME_WINDOW_SECONDS": "1h"}), []string{"TIME_WINDOW_SECONDS must be an integer"}},
		{"float window", required(map[string]string{"TIME_WINDOW_SECONDS": "1.5"}), []string{"TIME_WINDOW_SECONDS must be an integer"}},
		{"overflow window", required(map[string]string{"TIME_WINDOW_SECONDS": "99999999999999999999"}), []string{"TIME_WINDOW_SECONDS must be an integer"}},
		{"negative skew", required(map[string]string{"ALLOWED_CLOCK_SKEW": "-1"}), []string{"ALLOWED_CLOCK_SKEW must be >= 0"}},
		{"non numeric skew", required(map[string]string{"ALLOWED_CLOCK_SKEW": "one"}), []string{"ALLOWED_CLOCK_SKEW must be an integer"}},
		{"zero body", required(map[string]string{"MAX_BODY_BYTES": "0"}), []string{"MAX_BODY_BYTES must be >= 1"}},
		{"non numeric body", required(map[string]string{"MAX_BODY_BYTES": "1MB"}), []string{"MAX_BODY_BYTES must be an integer"}},
		{
			"reports every problem",
			map[string]string{"TIME_WINDOW_SECONDS": "0", "ALLOWED_CLOCK_SKEW": "x", "MAX_BODY_BYTES": "-1"},
			[]string{"JWT_SECRET", "TIME_TOKEN_SECRET", "TIME_WINDOW_SECONDS", "ALLOWED_CLOCK_SKEW", "MAX_BODY_BYTES"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(env(tt.values))
			if err == nil {
				t.Fatalf("Load returned nil error and config %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("Load returned config %+v alongside error", cfg)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestSplitList(t *testing.T) {
	tests := map[string][]string{
		"":          nil,
		" , ,":      nil,
		"a":         {"a"},
		"a,b":       {"a", "b"},
		" a , b ,c": {"a", "b", "c"},
	}
	for raw, want := range tests {
		if got := splitList(raw); !slices.Equal(got, want) {
			t.Errorf("splitList(%q) = %v, want %v", raw, got, want)
		}
	}
}
