// Copyright 2025 Ricardo L. Olsen. All rights reserved.
// Use of this source code is governed by a version 3 of the GNU General
// Public License, license that can be found in the LICENSE file.

package cs101

import (
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
)

// ClientOption client (primary station) configuration options
type ClientOption struct {
	config            Config
	params            asdu.Params
	autoReconnect     bool          // Whether to attempt reconnection on serial port errors
	reconnectInterval time.Duration // Reconnection attempt interval
	secondaryAddrs    []uint16      // Link addresses of the secondary stations to serve (unbalanced multi-drop)
}

// NewOption creates a new ClientOption with default CS101 config and standard CS101 ASDU params.
// Note: SerialConfig within the default config needs to be set explicitly using SetSerialConfig.
func NewOption() *ClientOption {
	return &ClientOption{
		config:            DefaultConfig(),
		params:            *asdu.ParamsStandard101, // Use CS101 standard params
		autoReconnect:     true,
		reconnectInterval: DefaultReconnectInterval,
	}
}

// SetConfig sets the main CS101 configuration. Uses DefaultConfig() if the provided cfg is invalid.
func (sf *ClientOption) SetConfig(cfg Config) (err error) {
	if err = cfg.Valid(); err != nil {
		sf.config = DefaultConfig()
	} else {
		sf.config = cfg
	}
	return
}

// SetParams sets the ASDU parameters. Uses asdu.ParamsStandard101 if the provided p is invalid.
func (sf *ClientOption) SetParams(p *asdu.Params) (err error) {
	if err = p.Valid(); err != nil {
		sf.params = *asdu.ParamsStandard101
	} else {
		sf.params = *p
	}
	return
}

// SetReconnectInterval sets the interval for attempting reconnection after a connection failure.
func (sf *ClientOption) SetReconnectInterval(t time.Duration) *ClientOption {
	if t > 0 {
		sf.reconnectInterval = t
	}
	return sf
}

// SetAutoReconnect enables or disables automatic reconnection attempts.
func (sf *ClientOption) SetAutoReconnect(b bool) *ClientOption {
	sf.autoReconnect = b
	return sf
}

// AddSecondaryAddress adds the link address of a secondary station to the
// set of stations the primary polls (unbalanced multi-drop). If no address
// is added, Config.LinkAddress is used as the single secondary station.
// Duplicate addresses are ignored. Not used in balanced mode.
func (sf *ClientOption) AddSecondaryAddress(addr uint16) *ClientOption {
	for _, a := range sf.secondaryAddrs {
		if a == addr {
			return sf
		}
	}
	sf.secondaryAddrs = append(sf.secondaryAddrs, addr)
	return sf
}
