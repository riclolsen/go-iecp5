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
func (sf *ClientOption) SetConfig(cfg Config) *ClientOption {
	if err := cfg.Valid(); err != nil {
		sf.config = DefaultConfig()
	} else {
		sf.config = cfg
	}
	return sf
}

// SetSerialConfig sets the serial port configuration within the main config.
func (sf *ClientOption) SetSerialConfig(serialCfg SerialConfig) *ClientOption {
	sf.config.Serial = serialCfg
	// TODO: Optionally re-validate the main config here?
	// if err := sf.config.Valid(); err != nil { ... }
	return sf
}

// SetParams sets the ASDU parameters. Uses asdu.ParamsStandard101 if the provided p is invalid.
func (sf *ClientOption) SetParams(p *asdu.Params) *ClientOption {
	if err := p.Valid(); err != nil {
		sf.params = *asdu.ParamsStandard101
	} else {
		sf.params = *p
	}
	return sf
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
