package main

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/riclolsen/go-iecp5/asdu"
	"github.com/riclolsen/go-iecp5/cs104"
)

// sendTimeout bounds how long a reply waits for send-buffer room. A master
// that has stopped acknowledging must not block the simulation for ever.
const sendTimeout = 30 * time.Second

// The simulated database.
//
// It exists to give a master something worth looking at: every monitored type
// this library supports, at addresses grouped by type, with values that move
// and quality descriptors that cover every flag. A simulator where everything
// reads GOOD and nothing changes cannot tell you whether the master you are
// testing renders quality at all.
//
// Address plan (common address 1):
//
//	   0        M_EI_NA_1  end of initialization, sent once per connection
//	1001..1012  M_SP_NA_1  single point
//	1101..1104  M_SP_TA_1  single point, CP24Time2a          (spontaneous)
//	1201..1208  M_SP_TB_1  single point, CP56Time2a          (spontaneous)
//	2001..2008  M_DP_NA_1  double point
//	2201..2204  M_DP_TB_1  double point, CP56Time2a          (spontaneous)
//	3001..3004  M_ST_NA_1  step position
//	3201..3202  M_ST_TB_1  step position, CP56Time2a         (spontaneous)
//	3501..3504  M_BO_NA_1  bit string of 32 bits
//	3601..3602  M_BO_TB_1  bit string, CP56Time2a            (spontaneous)
//	4001..4008  M_ME_NA_1  measured value, normalized
//	4101..4104  M_ME_ND_1  measured value, normalized, no quality descriptor
//	4201..4204  M_ME_TD_1  measured value, normalized, CP56  (spontaneous)
//	4501..4508  M_ME_NB_1  measured value, scaled
//	4601..4604  M_ME_TE_1  measured value, scaled, CP56      (spontaneous)
//	5001..5012  M_ME_NC_1  measured value, short float
//	5101..5106  M_ME_TF_1  measured value, short float, CP56 (spontaneous)
//	6001..6006  M_IT_NA_1  integrated totals                 (counter interrogation)
//	6101..6102  M_IT_TB_1  integrated totals, CP56           (counter interrogation)
//	6501..6502  M_PS_NA_1  packed single points with status change detection
//	7001..7004  M_EP_TD_1  protection equipment event, CP56  (spontaneous)
//	7101        M_EP_TE_1  packed start events, CP56         (spontaneous)
//	7201        M_EP_TF_1  packed output circuit info, CP56  (spontaneous)
//	7301        M_EP_TA_1  protection equipment event, CP24  (spontaneous)
//	7401        M_EP_TB_1  packed start events, CP24         (spontaneous)
//	7501        M_EP_TC_1  packed output circuit info, CP24  (spontaneous)
//	9001..9601  commands — see commands.go
//
// Time-tagged types are never part of an interrogation reply: the standard
// answers a general interrogation with the untagged variants and uses the
// tagged ones for spontaneous reporting, which is what this does.
const simCA = asdu.CommonAddr(1)

// Address bases, one block per type.
const (
	ioaSingle       = 1001
	ioaSingleCP24   = 1101
	ioaSingleCP56   = 1201
	ioaDouble       = 2001
	ioaDoubleCP56   = 2201
	ioaStep         = 3001
	ioaStepCP56     = 3201
	ioaBits         = 3501
	ioaBitsCP56     = 3601
	ioaNormal       = 4001
	ioaNormalNoQual = 4101
	ioaNormalCP56   = 4201
	ioaScaled       = 4501
	ioaScaledCP56   = 4601
	ioaFloat        = 5001
	ioaFloatCP56    = 5101
	ioaCounter      = 6001
	ioaCounterCP56  = 6101
	ioaPackedSCD    = 6501
	ioaProtEvent    = 7001
	ioaProtStart    = 7101
	ioaProtOutput   = 7201
	ioaProtEvent24  = 7301
	ioaProtStart24  = 7401
	ioaProtOutput24 = 7501
)

// How many objects each block holds.
const (
	nSingle       = 12
	nSingleCP24   = 4
	nSingleCP56   = 8
	nDouble       = 8
	nDoubleCP56   = 4
	nStep         = 4
	nStepCP56     = 2
	nBits         = 4
	nBitsCP56     = 2
	nNormal       = 8
	nNormalNoQual = 4
	nNormalCP56   = 4
	nScaled       = 8
	nScaledCP56   = 4
	nFloat        = 12
	nFloatCP56    = 6
	nCounter      = 6
	nCounterCP56  = 2
	nPackedSCD    = 2
	nProtEvent    = 4
)

// qualityCycle is one of every quality descriptor a monitored value can
// carry, including a combination, so a master has all of them on screen at
// once rather than only when a device happens to misbehave.
var qualityCycle = []asdu.QualityDescriptor{
	asdu.QDSGood,
	asdu.QDSGood,
	asdu.QDSOverflow,
	asdu.QDSBlocked,
	asdu.QDSSubstituted,
	asdu.QDSNotTopical,
	asdu.QDSInvalid,
	asdu.QDSInvalid | asdu.QDSNotTopical,
}

// qualityFor spreads the cycle across a block, leaving index 0 good so that
// the first row of every block reads normally.
func qualityFor(i int) asdu.QualityDescriptor {
	return qualityCycle[i%len(qualityCycle)]
}

// qdpCycle is the same idea for protection equipment quality.
var qdpCycle = []asdu.QualityDescriptorProtection{
	asdu.QDPGood,
	asdu.QDPElapsedTimeInvalid,
	asdu.QDPBlocked,
	asdu.QDPSubstituted,
	asdu.QDPNotTopical,
	asdu.QDPInvalid,
}

func qdpFor(i int) asdu.QualityDescriptorProtection {
	return qdpCycle[i%len(qdpCycle)]
}

// sim holds the whole simulated process. Everything is read and written under
// one mutex: the ticker writes it and the connection goroutines read it.
type sim struct {
	mu sync.Mutex

	single       [nSingle]bool
	singleCP24   [nSingleCP24]bool
	singleCP56   [nSingleCP56]bool
	double       [nDouble]asdu.DoublePoint
	doubleCP56   [nDoubleCP56]asdu.DoublePoint
	step         [nStep]asdu.StepPosition
	stepCP56     [nStepCP56]asdu.StepPosition
	bits         [nBits]uint32
	bitsCP56     [nBitsCP56]uint32
	normal       [nNormal]asdu.Normalize
	normalNoQual [nNormalNoQual]asdu.Normalize
	normalCP56   [nNormalCP56]asdu.Normalize
	scaled       [nScaled]int16
	scaledCP56   [nScaledCP56]int16
	floats       [nFloat]float32
	floatsCP56   [nFloatCP56]float32
	counters     [nCounter]int32
	countersCP56 [nCounterCP56]int32
	scd          [nPackedSCD]asdu.StatusAndStatusChangeDetection

	// intermittent marks the block index whose quality is currently forced
	// invalid, so a master watching quality sees it change rather than only
	// ever seeing a static pattern.
	intermittent int

	tick  uint64
	phase float64
	rnd   *rand.Rand

	// copies repeats the whole address plan at multiples of ioaStride, so
	// this example can stand in for a large outstation database. The values
	// are shared between copies; only the addresses differ.
	copies int
}

// ioaStride is the gap between repeated copies of the address plan. The plan
// spans 0..9601, so 10000 keeps each copy's addresses readable: copy 3's
// single points are at 31001..31012.
const ioaStride = 10000

// offsets is the base address of each copy of the plan.
func (s *sim) offsets() []int {
	n := s.copies
	if n < 1 {
		n = 1
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i * ioaStride
	}
	return out
}

func newSim(copies int) *sim {
	if copies < 1 {
		copies = 1
	}
	s := &sim{rnd: rand.New(rand.NewSource(1)), copies: copies}

	// Digitals start in a mixture of states rather than all off.
	for i := range s.single {
		s.single[i] = i%3 == 0
	}
	for i := range s.singleCP24 {
		s.singleCP24[i] = i%2 == 0
	}
	for i := range s.singleCP56 {
		s.singleCP56[i] = i%2 == 1
	}
	// Double points cycle through all four states, including the two that
	// mean "the device cannot tell" — a master that renders only ON and OFF
	// is a master that hides a stuck disconnector.
	states := []asdu.DoublePoint{
		asdu.DPIDeterminedOn, asdu.DPIDeterminedOff,
		asdu.DPIIndeterminateOrIntermediate, asdu.DPIIndeterminate,
	}
	for i := range s.double {
		s.double[i] = states[i%len(states)]
	}
	for i := range s.doubleCP56 {
		s.doubleCP56[i] = states[(i+1)%len(states)]
	}
	for i := range s.step {
		s.step[i] = asdu.StepPosition{Val: i*8 - 12, HasTransient: i == 2}
	}
	for i := range s.stepCP56 {
		s.stepCP56[i] = asdu.StepPosition{Val: 10 * (i + 1)}
	}
	for i := range s.bits {
		s.bits[i] = 0x0F0F0F0F >> uint(i)
	}
	for i := range s.bitsCP56 {
		s.bitsCP56[i] = 0xA5A50000 | uint32(i)
	}
	for i := range s.counters {
		s.counters[i] = int32(10000 * (i + 1))
	}
	for i := range s.countersCP56 {
		s.countersCP56[i] = int32(500000 * (i + 1))
	}
	for i := range s.scd {
		s.scd[i] = asdu.StatusAndStatusChangeDetection(0x0000FFFF << uint(i))
	}
	s.advance() // give everything a value before the first interrogation
	return s
}

// advance moves the process on one step.
func (s *sim) advance() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tick++
	s.phase += 0.12

	// Analogues: each point gets its own period and amplitude, so a screenful
	// of them does not look like one signal repeated.
	for i := range s.normal {
		s.normal[i] = asdu.Normalize(30000 * math.Sin(s.phase*(0.4+0.2*float64(i))))
	}
	for i := range s.normalNoQual {
		s.normalNoQual[i] = asdu.Normalize(20000 * math.Cos(s.phase*(0.3+0.3*float64(i))))
	}
	for i := range s.normalCP56 {
		s.normalCP56[i] = asdu.Normalize(25000 * math.Sin(s.phase*0.7+float64(i)))
	}
	for i := range s.scaled {
		s.scaled[i] = int16(1000*math.Sin(s.phase*(0.5+0.15*float64(i))) + float64(200*i))
	}
	// Two scaled points sit near the ends of the range, where a master that
	// mishandles the sign or the width shows it.
	s.scaled[nScaled-1] = int16(32767 - int(s.tick%50))
	s.scaled[nScaled-2] = int16(-32768 + int(s.tick%50))
	for i := range s.scaledCP56 {
		s.scaledCP56[i] = int16(500 * math.Cos(s.phase*(0.6+0.2*float64(i))))
	}

	// Floats span the magnitudes a substation actually reports: line voltage,
	// current, power, frequency, power factor, and a negative flow.
	base := []float64{11000, 230.4, 49.98, 0.92, -12.5, 1.5e6, 0.0042, 380, 63.1, 100, 12.75, -0.5}
	for i := range s.floats {
		amp := math.Abs(base[i%len(base)]) * 0.02
		s.floats[i] = float32(base[i%len(base)] + amp*math.Sin(s.phase*(0.3+0.1*float64(i))))
	}
	for i := range s.floatsCP56 {
		s.floatsCP56[i] = float32(base[i%len(base)] * (1 + 0.01*math.Sin(s.phase+float64(i))))
	}

	// Counters only ever go up, which is the point of a counter.
	for i := range s.counters {
		s.counters[i] += int32(s.rnd.Intn(5) + 1)
	}
	for i := range s.countersCP56 {
		s.countersCP56[i] += int32(s.rnd.Intn(3) + 1)
	}

	// Step positions walk up and down their range.
	for i := range s.step {
		v := s.step[i].Val + 1
		if v > 20 {
			v = -20
		}
		s.step[i] = asdu.StepPosition{Val: v, HasTransient: v%7 == 0}
	}

	// Bit strings rotate, so every bit is set at some point.
	for i := range s.bits {
		s.bits[i] = s.bits[i]<<1 | s.bits[i]>>31
	}

	// Status change detection: the low half is the status, the high half
	// marks which of those bits changed since the last report.
	for i := range s.scd {
		old := uint32(s.scd[i])
		status := old&0xFFFF ^ (1 << uint(s.tick%16))
		s.scd[i] = asdu.StatusAndStatusChangeDetection(status | (old&0xFFFF^status)<<16)
	}

	// Digitals flip on their own schedules.
	for i := range s.single {
		if s.tick%uint64(3+i) == 0 {
			s.single[i] = !s.single[i]
		}
	}
	for i := range s.singleCP56 {
		if s.tick%uint64(5+i) == 0 {
			s.singleCP56[i] = !s.singleCP56[i]
		}
	}
	for i := range s.singleCP24 {
		if s.tick%uint64(7+i) == 0 {
			s.singleCP24[i] = !s.singleCP24[i]
		}
	}
	if s.tick%4 == 0 {
		for i := range s.double {
			s.double[i] = asdu.DoublePoint((uint(s.double[i]) + 1) % 4)
		}
	}

	// One point at a time is forced invalid, so quality is seen changing.
	if s.tick%10 == 0 {
		s.intermittent = (s.intermittent + 1) % nFloat
	}
}

// quality returns the descriptor for a block index, including the live
// intermittent fault.
func (s *sim) quality(block string, i int) asdu.QualityDescriptor {
	q := qualityFor(i)
	if block == "float" && i == s.intermittent {
		q |= asdu.QDSInvalid
	}
	return q
}

// ---------- interrogation ----------

// interrogation sends every object a general interrogation answers: the
// untagged monitored types. Counters are not included — counter
// interrogation returns those.
func (s *sim) interrogation(c asdu.Connect, cause asdu.CauseOfTransmission) {
	for _, off := range s.offsets() {
		s.interrogationAt(c, cause, off)
	}
}

func (s *sim) interrogationAt(c asdu.Connect, cause asdu.CauseOfTransmission, off int) {
	// A large database does not fit in the session's send buffer, so the
	// sends below wait for room rather than being refused and lost.
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	c = cs104.Waiting(ctx, c)

	s.mu.Lock()
	single, double := s.single, s.double
	step, bits := s.step, s.bits
	normal, normalNoQual := s.normal, s.normalNoQual
	scaled, floats, scd := s.scaled, s.floats, s.scd
	intermittent := s.intermittent
	s.mu.Unlock()

	sp := make([]asdu.SinglePointInfo, 0, nSingle)
	for i, v := range single {
		sp = append(sp, asdu.SinglePointInfo{
			Ioa: asdu.InfoObjAddr(ioaSingle + i + off), Value: v, Qds: qualityFor(i)})
	}
	emit(func() error { return asdu.Single(c, false, cause, simCA, sp...) })

	dp := make([]asdu.DoublePointInfo, 0, nDouble)
	for i, v := range double {
		dp = append(dp, asdu.DoublePointInfo{
			Ioa: asdu.InfoObjAddr(ioaDouble + i + off), Value: v, Qds: qualityFor(i)})
	}
	emit(func() error { return asdu.Double(c, false, cause, simCA, dp...) })

	st := make([]asdu.StepPositionInfo, 0, nStep)
	for i, v := range step {
		st = append(st, asdu.StepPositionInfo{
			Ioa: asdu.InfoObjAddr(ioaStep + i + off), Value: v, Qds: qualityFor(i)})
	}
	emit(func() error { return asdu.Step(c, false, cause, simCA, st...) })

	bs := make([]asdu.BitString32Info, 0, nBits)
	for i, v := range bits {
		bs = append(bs, asdu.BitString32Info{
			Ioa: asdu.InfoObjAddr(ioaBits + i + off), Value: v, Qds: qualityFor(i)})
	}
	emit(func() error { return asdu.BitString32(c, false, cause, simCA, bs...) })

	nv := make([]asdu.MeasuredValueNormalInfo, 0, nNormal)
	for i, v := range normal {
		nv = append(nv, asdu.MeasuredValueNormalInfo{
			Ioa: asdu.InfoObjAddr(ioaNormal + i + off), Value: v, Qds: qualityFor(i)})
	}
	emit(func() error { return asdu.MeasuredValueNormal(c, false, cause, simCA, nv...) })

	// M_ME_ND_1 carries no quality descriptor at all: the master should show
	// it as having none rather than inventing GOOD.
	nq := make([]asdu.MeasuredValueNormalInfo, 0, nNormalNoQual)
	for i, v := range normalNoQual {
		nq = append(nq, asdu.MeasuredValueNormalInfo{
			Ioa: asdu.InfoObjAddr(ioaNormalNoQual + i + off), Value: v})
	}
	emit(func() error { return asdu.MeasuredValueNormalNoQuality(c, false, cause, simCA, nq...) })

	sv := make([]asdu.MeasuredValueScaledInfo, 0, nScaled)
	for i, v := range scaled {
		sv = append(sv, asdu.MeasuredValueScaledInfo{
			Ioa: asdu.InfoObjAddr(ioaScaled + i + off), Value: v, Qds: qualityFor(i)})
	}
	emit(func() error { return asdu.MeasuredValueScaled(c, false, cause, simCA, sv...) })

	// The float block is split so one ASDU stays inside the 249 octet limit
	// with room to spare.
	fv := make([]asdu.MeasuredValueFloatInfo, 0, nFloat)
	for i, v := range floats {
		q := qualityFor(i)
		if i == intermittent {
			q |= asdu.QDSInvalid
		}
		fv = append(fv, asdu.MeasuredValueFloatInfo{
			Ioa: asdu.InfoObjAddr(ioaFloat + i + off), Value: v, Qds: q})
	}
	emit(func() error { return asdu.MeasuredValueFloat(c, false, cause, simCA, fv[:6]...) })
	emit(func() error { return asdu.MeasuredValueFloat(c, false, cause, simCA, fv[6:]...) })

	ps := make([]asdu.PackedSinglePointWithSCDInfo, 0, nPackedSCD)
	for i, v := range scd {
		ps = append(ps, asdu.PackedSinglePointWithSCDInfo{
			Ioa: asdu.InfoObjAddr(ioaPackedSCD + i + off), Scd: v, Qds: qualityFor(i)})
	}
	emit(func() error { return asdu.PackedSinglePointWithSCD(c, false, cause, simCA, ps...) })
}

// counterInterrogation returns the integrated totals, with the counter's own
// flags: one is adjusted, one carries, one is invalid.
func (s *sim) counterInterrogation(c asdu.Connect, cause asdu.CauseOfTransmission) {
	for _, off := range s.offsets() {
		s.counterInterrogationAt(c, cause, off)
	}
}

func (s *sim) counterInterrogationAt(c asdu.Connect, cause asdu.CauseOfTransmission, off int) {
	// A large database does not fit in the session's send buffer, so the
	// sends below wait for room rather than being refused and lost.
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	c = cs104.Waiting(ctx, c)

	s.mu.Lock()
	counters, countersCP56 := s.counters, s.countersCP56
	seq := byte(s.tick % 32)
	s.mu.Unlock()

	it := make([]asdu.BinaryCounterReadingInfo, 0, nCounter)
	for i, v := range counters {
		it = append(it, asdu.BinaryCounterReadingInfo{
			Ioa: asdu.InfoObjAddr(ioaCounter + i + off),
			Value: asdu.BinaryCounterReading{
				CounterReading: v, SeqNumber: seq,
				HasCarry:   i == 1,
				IsAdjusted: i == 2,
				IsInvalid:  i == 3,
			}})
	}
	emit(func() error { return asdu.IntegratedTotals(c, false, cause, simCA, it...) })

	now := time.Now()
	for i, v := range countersCP56 {
		emit(func() error {
			return asdu.IntegratedTotalsCP56Time2a(c, cause, simCA,
				asdu.BinaryCounterReadingInfo{
					Ioa:   asdu.InfoObjAddr(ioaCounterCP56 + i + off),
					Value: asdu.BinaryCounterReading{CounterReading: v, SeqNumber: seq},
					Time:  now,
				})
		})
	}
}

// ---------- spontaneous reporting ----------

// spontaneous sends the time-tagged types and the protection equipment
// events. These are never part of an interrogation reply, so this is the only
// place they appear.
func (s *sim) spontaneous(c asdu.Connect) {
	for _, off := range s.offsets() {
		s.spontaneousAt(c, off)
	}
}

// spontaneousAt reports the time-tagged types and the protection events. The
// caller supplies a Connect that waits for send-buffer room.
func (s *sim) spontaneousAt(c asdu.Connect, off int) {

	s.mu.Lock()
	singleCP24, singleCP56 := s.singleCP24, s.singleCP56
	doubleCP56, stepCP56, bitsCP56 := s.doubleCP56, s.stepCP56, s.bitsCP56
	normalCP56, scaledCP56, floatsCP56 := s.normalCP56, s.scaledCP56, s.floatsCP56
	tick := s.tick
	s.mu.Unlock()

	spont := asdu.CauseOfTransmission{Cause: asdu.Spontaneous}
	now := time.Now()

	// Single points with both time tag widths.
	for i, v := range singleCP56 {
		emit(func() error {
			return asdu.SingleCP56Time2a(c, spont, simCA, asdu.SinglePointInfo{
				Ioa: asdu.InfoObjAddr(ioaSingleCP56 + i + off), Value: v,
				Qds: qualityFor(i), Time: now})
		})
	}
	if tick%3 == 0 {
		for i, v := range singleCP24 {
			emit(func() error {
				return asdu.SingleCP24Time2a(c, spont, simCA, asdu.SinglePointInfo{
					Ioa: asdu.InfoObjAddr(ioaSingleCP24 + i + off), Value: v,
					Qds: qualityFor(i), Time: now})
			})
		}
	}

	for i, v := range doubleCP56 {
		emit(func() error {
			return asdu.DoubleCP56Time2a(c, spont, simCA, asdu.DoublePointInfo{
				Ioa: asdu.InfoObjAddr(ioaDoubleCP56 + i + off), Value: v,
				Qds: qualityFor(i), Time: now})
		})
	}
	for i, v := range stepCP56 {
		emit(func() error {
			return asdu.StepCP56Time2a(c, spont, simCA, asdu.StepPositionInfo{
				Ioa: asdu.InfoObjAddr(ioaStepCP56 + i + off), Value: v,
				Qds: qualityFor(i), Time: now})
		})
	}
	for i, v := range bitsCP56 {
		emit(func() error {
			return asdu.BitString32CP56Time2a(c, spont, simCA, asdu.BitString32Info{
				Ioa: asdu.InfoObjAddr(ioaBitsCP56 + i + off), Value: v,
				Qds: qualityFor(i), Time: now})
		})
	}
	for i, v := range normalCP56 {
		emit(func() error {
			return asdu.MeasuredValueNormalCP56Time2a(c, spont, simCA,
				asdu.MeasuredValueNormalInfo{
					Ioa: asdu.InfoObjAddr(ioaNormalCP56 + i + off), Value: v,
					Qds: qualityFor(i), Time: now})
		})
	}
	for i, v := range scaledCP56 {
		emit(func() error {
			return asdu.MeasuredValueScaledCP56Time2a(c, spont, simCA,
				asdu.MeasuredValueScaledInfo{
					Ioa: asdu.InfoObjAddr(ioaScaledCP56 + i + off), Value: v,
					Qds: qualityFor(i), Time: now})
		})
	}
	for i, v := range floatsCP56 {
		emit(func() error {
			return asdu.MeasuredValueFloatCP56Time2a(c, spont, simCA,
				asdu.MeasuredValueFloatInfo{
					Ioa: asdu.InfoObjAddr(ioaFloatCP56 + i + off), Value: v,
					Qds: qualityFor(i), Time: now})
		})
	}

	// Protection equipment: a relay reports an event, which phases started,
	// and which output circuits it drove — each with its own elapsed time.
	if tick%5 == 0 {
		s.protection(c, now, tick, off)
	}
}

// protection sends the six protection equipment types.
func (s *sim) protection(c asdu.Connect, now time.Time, tick uint64, off int) {
	spont := asdu.CauseOfTransmission{Cause: asdu.Spontaneous}

	events := []asdu.SingleEvent{
		asdu.SEDeterminedOn, asdu.SEDeterminedOff,
		asdu.SEIndeterminateOrIntermediate, asdu.SEIndeterminate,
	}
	ev := make([]asdu.EventOfProtectionEquipmentInfo, 0, nProtEvent)
	for i := 0; i < nProtEvent; i++ {
		ev = append(ev, asdu.EventOfProtectionEquipmentInfo{
			Ioa:   asdu.InfoObjAddr(ioaProtEvent + i + off),
			Event: events[(int(tick)+i)%len(events)],
			Qdp:   qdpFor(i),
			Msec:  uint16(40 + 17*i),
			Time:  now,
		})
	}
	emit(func() error { return asdu.EventOfProtectionEquipmentCP56Time2a(c, spont, simCA, ev...) })

	// Which phases started: the flags are a set, not an enumeration.
	start := asdu.SEPGeneralStart | asdu.SEPStartL1
	switch tick / 5 % 4 {
	case 1:
		start |= asdu.SEPStartL2
	case 2:
		start |= asdu.SEPStartL3 | asdu.SEPStartEarthCurrent
	case 3:
		start |= asdu.SEPStartReverseDirection
	}
	emit(func() error {
		return asdu.PackedStartEventsOfProtectionEquipmentCP56Time2a(c, spont, simCA,
			asdu.PackedStartEventsOfProtectionEquipmentInfo{
				Ioa: asdu.InfoObjAddr(ioaProtStart + off), Event: start, Qdp: qdpFor(int(tick)),
				Msec: uint16(60 + tick%40), Time: now})
	})

	oci := asdu.OCIGeneralCommand
	switch tick / 5 % 3 {
	case 1:
		oci |= asdu.OCICommandL1 | asdu.OCICommandL2
	case 2:
		oci |= asdu.OCICommandL3
	}
	emit(func() error {
		return asdu.PackedOutputCircuitInfoCP56Time2a(c, spont, simCA,
			asdu.PackedOutputCircuitInfoInfo{
				Ioa: asdu.InfoObjAddr(ioaProtOutput + off), Oci: oci, Qdp: qdpFor(int(tick) + 1),
				Msec: uint16(25 + tick%30), Time: now})
	})

	// The CP24Time2a variants of the same three, so a master that only
	// implements one time tag width is found out.
	emit(func() error {
		return asdu.EventOfProtectionEquipmentCP24Time2a(c, spont, simCA,
			asdu.EventOfProtectionEquipmentInfo{
				Ioa: asdu.InfoObjAddr(ioaProtEvent24 + off), Event: events[int(tick)%len(events)],
				Qdp: qdpFor(2), Msec: 33, Time: now})
	})
	emit(func() error {
		return asdu.PackedStartEventsOfProtectionEquipmentCP24Time2a(c, spont, simCA,
			asdu.PackedStartEventsOfProtectionEquipmentInfo{
				Ioa: asdu.InfoObjAddr(ioaProtStart24 + off), Event: asdu.SEPGeneralStart | asdu.SEPStartEarthCurrent,
				Qdp: qdpFor(3), Msec: 44, Time: now})
	})
	emit(func() error {
		return asdu.PackedOutputCircuitInfoCP24Time2a(c, spont, simCA,
			asdu.PackedOutputCircuitInfoInfo{
				Ioa: asdu.InfoObjAddr(ioaProtOutput24 + off), Oci: asdu.OCIGeneralCommand | asdu.OCICommandL3,
				Qdp: qdpFor(4), Msec: 55, Time: now})
	})
}

// endOfInitialization is what a device says when it comes up, and what a
// master uses to know its picture is stale.
func (s *sim) endOfInitialization(c asdu.Connect) {
	emit(func() error {
		return asdu.EndOfInitialization(c, asdu.CauseOfTransmission{}, simCA, 0,
			asdu.CauseOfInitial{Cause: asdu.COILocalPowerOn})
	})
}

// totalObjects is how many information objects the simulation holds, for the
// start-up banner.
func totalObjects(copies int) int {
	if copies < 1 {
		copies = 1
	}
	return copies * (nSingle + nSingleCP24 + nSingleCP56 +
		nDouble + nDoubleCP56 + nStep + nStepCP56 + nBits + nBitsCP56 +
		nNormal + nNormalNoQual + nNormalCP56 + nScaled + nScaledCP56 +
		nFloat + nFloatCP56 + nCounter + nCounterCP56 + nPackedSCD +
		nProtEvent + 5) // the five single-object protection types
}

// readOne answers a read command (C_RD_NA_1) for a single address, which is
// how a master asks about one object instead of interrogating everything.
// It reports whether the address is one this simulator holds.
func (s *sim) readOne(c asdu.Connect, ioa asdu.InfoObjAddr) bool {
	req := asdu.CauseOfTransmission{Cause: asdu.Request}
	// Reduce the address to its offset within one copy of the plan.
	i := int(ioa) % ioaStride
	if int(ioa)/ioaStride >= s.copies {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case i >= ioaSingle && i < ioaSingle+nSingle:
		n := i - ioaSingle
		emit(func() error {
			return asdu.Single(c, false, req, simCA, asdu.SinglePointInfo{
				Ioa: ioa, Value: s.single[n], Qds: qualityFor(n)})
		})
	case i >= ioaDouble && i < ioaDouble+nDouble:
		n := i - ioaDouble
		emit(func() error {
			return asdu.Double(c, false, req, simCA, asdu.DoublePointInfo{
				Ioa: ioa, Value: s.double[n], Qds: qualityFor(n)})
		})
	case i >= ioaStep && i < ioaStep+nStep:
		n := i - ioaStep
		emit(func() error {
			return asdu.Step(c, false, req, simCA, asdu.StepPositionInfo{
				Ioa: ioa, Value: s.step[n], Qds: qualityFor(n)})
		})
	case i >= ioaBits && i < ioaBits+nBits:
		n := i - ioaBits
		emit(func() error {
			return asdu.BitString32(c, false, req, simCA, asdu.BitString32Info{
				Ioa: ioa, Value: s.bits[n], Qds: qualityFor(n)})
		})
	case i >= ioaNormal && i < ioaNormal+nNormal:
		n := i - ioaNormal
		emit(func() error {
			return asdu.MeasuredValueNormal(c, false, req, simCA, asdu.MeasuredValueNormalInfo{
				Ioa: ioa, Value: s.normal[n], Qds: qualityFor(n)})
		})
	case i >= ioaNormalNoQual && i < ioaNormalNoQual+nNormalNoQual:
		n := i - ioaNormalNoQual
		emit(func() error {
			return asdu.MeasuredValueNormalNoQuality(c, false, req, simCA,
				asdu.MeasuredValueNormalInfo{Ioa: ioa, Value: s.normalNoQual[n]})
		})
	case i >= ioaScaled && i < ioaScaled+nScaled:
		n := i - ioaScaled
		emit(func() error {
			return asdu.MeasuredValueScaled(c, false, req, simCA, asdu.MeasuredValueScaledInfo{
				Ioa: ioa, Value: s.scaled[n], Qds: qualityFor(n)})
		})
	case i >= ioaFloat && i < ioaFloat+nFloat:
		n := i - ioaFloat
		emit(func() error {
			return asdu.MeasuredValueFloat(c, false, req, simCA, asdu.MeasuredValueFloatInfo{
				Ioa: ioa, Value: s.floats[n], Qds: qualityFor(n)})
		})
	case i >= ioaPackedSCD && i < ioaPackedSCD+nPackedSCD:
		n := i - ioaPackedSCD
		emit(func() error {
			return asdu.PackedSinglePointWithSCD(c, false, req, simCA,
				asdu.PackedSinglePointWithSCDInfo{Ioa: ioa, Scd: s.scd[n], Qds: qualityFor(n)})
		})
	default:
		return false
	}
	return true
}
