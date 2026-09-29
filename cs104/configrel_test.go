package cs104

import (
	"testing"
	"time"
)

// The four flow-control parameters are not independent. A combination that is
// individually in range can still be unusable, and the failure shows up as a
// healthy link dropping or stalling rather than as a configuration error.
func TestConfigRelationships(t *testing.T) {
	base := func() Config { return DefaultConfig() }

	for _, tt := range []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"the defaults", func(*Config) {}, false},

		// t₂ must be shorter than t₁.
		{"t2 equal to t1", func(c *Config) {
			c.RecvUnAckTimeout2 = c.SendUnAckTimeout1
		}, true},
		{"t2 longer than t1", func(c *Config) {
			c.SendUnAckTimeout1 = 10 * time.Second
			c.RecvUnAckTimeout2 = 20 * time.Second
		}, true},
		{"t2 just under t1", func(c *Config) {
			c.SendUnAckTimeout1 = 10 * time.Second
			c.RecvUnAckTimeout2 = 9 * time.Second
		}, false},

		// w above two thirds of k is advice, not an error: the
		// recommendation relates the peer's k to this station's w.
		{"w equal to k", func(c *Config) {
			c.RecvUnAckLimitW = c.SendUnAckLimitK
		}, false},
		{"k of 1 with w of 1", func(c *Config) {
			c.SendUnAckLimitK = 1
			c.RecvUnAckLimitW = 1
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			err := cfg.Valid()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Valid() = %v, wantErr %v (k=%d w=%d t1=%v t2=%v)",
					err, tt.wantErr, cfg.SendUnAckLimitK, cfg.RecvUnAckLimitW,
					cfg.SendUnAckTimeout1, cfg.RecvUnAckTimeout2)
			}
		})
	}
}

// The zero value must still fill in a usable, self-consistent set.
func TestZeroConfigIsValid(t *testing.T) {
	var cfg Config
	if err := cfg.Valid(); err != nil {
		t.Fatalf("the zero config must default to a usable one: %v", err)
	}
	if cfg.RecvUnAckTimeout2 >= cfg.SendUnAckTimeout1 {
		t.Errorf("defaults give t2=%v t1=%v", cfg.RecvUnAckTimeout2, cfg.SendUnAckTimeout1)
	}
	if advice := cfg.flowControlAdvice(); advice != "" {
		t.Errorf("defaults draw advice: %s", advice)
	}
}

// w ≤ ⅔·k is checked exactly, without integer truncation.
func TestFlowControlAdvice(t *testing.T) {
	for _, tt := range []struct {
		k, w   uint16
		advice bool
	}{
		{12, 8, false},
		{12, 9, true},
		{3, 2, false},
		{2, 1, false},
		{2, 2, true},
		{1, 1, true},
	} {
		cfg := Config{SendUnAckLimitK: tt.k, RecvUnAckLimitW: tt.w}
		if got := cfg.flowControlAdvice() != ""; got != tt.advice {
			t.Errorf("k=%d w=%d: advice %v, want %v", tt.k, tt.w, got, tt.advice)
		}
	}
}
