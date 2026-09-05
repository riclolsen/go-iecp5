package asdu

import (
	"net"
	"testing"
	"time"
)

// A control-direction system command may only carry the causes the standard
// defines for it. Every sender in csys.go constrains its cause — some by
// setting it, some by refusing a wrong one — except C_TS_TA_1, which passed
// the caller's cause through untouched: a request, a spontaneous, or even a
// monitor-direction error cause could be carried by a control ASDU.
//
// This table is the invariant. A timestamped variant added later without
// cause handling fails here.

// capture is a Connect that keeps whatever was sent.
type capture struct {
	p    *Params
	last *ASDU
}

func (c *capture) Params() *Params          { return c.p }
func (c *capture) UnderlyingConn() net.Conn { return nil }
func (c *capture) Send(u *ASDU) error {
	if _, err := u.MarshalBinary(); err != nil {
		return err
	}
	c.last = u
	return nil
}

func TestSystemCommandsConstrainTheirCause(t *testing.T) {
	stamp := time.Date(2026, 7, 15, 14, 30, 0, 0, time.UTC)

	for _, tt := range []struct {
		name string
		// allowed are the causes this ASDU may carry in the control
		// direction, per the companion standard and this file's own comments.
		allowed []Cause
		send    func(Connect, CauseOfTransmission) error
	}{
		{"C_IC_NA_1 interrogation", []Cause{Activation, Deactivation},
			func(c Connect, coa CauseOfTransmission) error {
				return InterrogationCmd(c, coa, 1, QOIStation)
			}},
		{"C_CI_NA_1 counter interrogation", []Cause{Activation},
			func(c Connect, coa CauseOfTransmission) error {
				return CounterInterrogationCmd(c, coa, 1, QualifierCountCall{})
			}},
		{"C_RD_NA_1 read", []Cause{Request},
			func(c Connect, coa CauseOfTransmission) error { return ReadCmd(c, coa, 1, 100) }},
		{"C_CS_NA_1 clock synchronisation", []Cause{Activation},
			func(c Connect, coa CauseOfTransmission) error {
				return ClockSynchronizationCmd(c, coa, 1, stamp)
			}},
		{"C_TS_NA_1 test command", []Cause{Activation},
			func(c Connect, coa CauseOfTransmission) error { return TestCommand(c, coa, 1) }},
		{"C_RP_NA_1 reset process", []Cause{Activation},
			func(c Connect, coa CauseOfTransmission) error {
				return ResetProcessCmd(c, coa, 1, QualifierOfResetProcessCmd(1))
			}},
		{"C_CD_NA_1 delay acquisition", []Cause{Spontaneous, Activation},
			func(c Connect, coa CauseOfTransmission) error {
				return DelayAcquireCommand(c, coa, 1, 500)
			}},
		{"C_TS_TA_1 test command, time tag", []Cause{Activation},
			func(c Connect, coa CauseOfTransmission) error {
				return TestCommandCP56Time2a(c, coa, 1, stamp)
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Whatever a caller passes, what goes out must be one of the
			// causes this ASDU may carry — either because the helper set it
			// or because it refused to send.
			for _, cause := range []Cause{
				Activation, Deactivation, Request, Spontaneous, Periodic,
				InterrogatedByStation, ActivationCon, UnknownTypeID, UnknownCOT,
			} {
				c := &capture{p: ParamsWide}
				err := tt.send(c, CauseOfTransmission{Cause: cause})
				if err != nil || c.last == nil {
					continue // refused, which is a correct answer
				}
				ok := false
				for _, a := range tt.allowed {
					if c.last.Coa.Cause == a {
						ok = true
					}
				}
				if !ok {
					t.Errorf("passing cause %d produced an ASDU carrying cause %d, allowed %v",
						byte(cause), byte(c.last.Coa.Cause), tt.allowed)
				}
			}

			// And the primary cause must still get through unaltered.
			c := &capture{p: ParamsWide}
			if err := tt.send(c, CauseOfTransmission{Cause: tt.allowed[0]}); err != nil {
				t.Fatalf("the primary cause was refused: %v", err)
			}
			if c.last == nil || c.last.Coa.Cause != tt.allowed[0] {
				t.Fatalf("the primary cause did not survive")
			}
		})
	}
}

// The two test-command helpers describe the same ASDU with and without a time
// tag, so they must agree about the cause.
func TestBothTestCommandsAgree(t *testing.T) {
	stamp := time.Date(2026, 7, 15, 14, 30, 0, 0, time.UTC)
	for _, cause := range []Cause{Activation, Request, Spontaneous, UnknownTypeID, Deactivation} {
		plain, tagged := &capture{p: ParamsWide}, &capture{p: ParamsWide}
		errP := TestCommand(plain, CauseOfTransmission{Cause: cause}, 1)
		errT := TestCommandCP56Time2a(tagged, CauseOfTransmission{Cause: cause}, 1, stamp)

		if (errP == nil) != (errT == nil) {
			t.Errorf("cause %d: C_TS_NA_1 err=%v but C_TS_TA_1 err=%v", byte(cause), errP, errT)
			continue
		}
		if errP != nil {
			continue
		}
		if plain.last.Coa.Cause != tagged.last.Coa.Cause {
			t.Errorf("cause %d: C_TS_NA_1 emitted %d, C_TS_TA_1 emitted %d",
				byte(cause), byte(plain.last.Coa.Cause), byte(tagged.last.Coa.Cause))
		}
	}
}
