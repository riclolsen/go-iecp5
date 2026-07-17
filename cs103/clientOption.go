// Copyright 2026 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs103

import (
	"time"
)

// DefaultReconnectInterval is the default serial reopen interval.
const DefaultReconnectInterval = 1 * time.Minute

// ClientOption is the primary station configuration options.
type ClientOption struct {
	config            Config
	autoReconnect     bool
	reconnectInterval time.Duration
	secondaryAddrs    []byte // link addresses of the protection devices to serve
}

// NewOption creates a ClientOption with the default CS103 configuration.
// SerialConfig must still be set via SetConfig.
func NewOption() *ClientOption {
	return &ClientOption{
		config:            DefaultConfig(),
		autoReconnect:     true,
		reconnectInterval: DefaultReconnectInterval,
	}
}

// SetConfig sets the CS103 configuration. Uses DefaultConfig() if the
// provided cfg is invalid.
func (sf *ClientOption) SetConfig(cfg Config) (err error) {
	if err = cfg.Valid(); err != nil {
		sf.config = DefaultConfig()
	} else {
		sf.config = cfg
	}
	return
}

// SetReconnectInterval sets the interval between serial reopen attempts.
func (sf *ClientOption) SetReconnectInterval(t time.Duration) *ClientOption {
	if t > 0 {
		sf.reconnectInterval = t
	}
	return sf
}

// SetAutoReconnect enables or disables automatic serial reopen attempts.
func (sf *ClientOption) SetAutoReconnect(b bool) *ClientOption {
	sf.autoReconnect = b
	return sf
}

// AddSecondaryAddress adds the link address of a protection device to the
// set of stations the primary polls (multi-drop). If no address is added,
// Config.LinkAddress is used as the single station. Duplicates are ignored.
func (sf *ClientOption) AddSecondaryAddress(addr byte) *ClientOption {
	for _, a := range sf.secondaryAddrs {
		if a == addr {
			return sf
		}
	}
	sf.secondaryAddrs = append(sf.secondaryAddrs, addr)
	return sf
}
