# Review & remediation plan — `f2fb2e3` "Fix: STOPDT confirmed while data was still outstanding"

- **Review date:** 2026-09-29
- **Commit reviewed:** `f2fb2e3` (HEAD, master)
- **Files in scope:** `cs104/config.go`, `cs104/server_session.go`, `cs104/configrel_test.go` (new), `cs104/stopdt_test.go` (new), `docs/cs104.md`
- **Status:** addressed 2026-09-29 — see *Resolution* at the end.

## Verification performed

| Check | Result |
|---|---|
| `go vet ./...` | clean |
| `go test ./...` | green — `asdu`, `cs101`, `cs103`, `cs104`, `filetransfer` |
| New tests (`TestConfigRelationships`, `TestZeroConfigIsValid`, `TestStopDtWaitsForOutstandingData`, `TestStopDtConfirmsImmediatelyWhenIdle`, `TestStopDtStopsNewDataImmediately`) | pass |
| `TestStopDtWaitsForOutstandingData` runtime | 3.30 s (see finding 10 on test timing) |

Findings come from static analysis of the actual code paths. No files were modified.

---

## What the change gets right

- **t₂ < t₁ is a genuine standard requirement**, not an invention. Vendor/standards-derived IEC 104 parameter tables state it explicitly (`t2 10s … t2 < t1`; Beckhoff profile: `t1: Max. timeout time for a response to a STARTDTAct, STOPDTAct or TESTFRAct frame`, `t2: An S frame is sent after this time, at the latest`). The defaults k=12, w=8, t₁=15 s, t₂=10 s, t₃=20 s satisfy it.
- **w ≤ ⅔·k matches the standard's wording** — the IEC-104 master driver manual states it as "*Recommendation: w should not exceed two-thirds of k*". It was already advice in `docs/cs104.md:215`; this formalises it.
- **`cs101` already enforced T2 < T1** (`cs101/config.go:164`), so the pattern has precedent in this repo.
- **Immediate cessation of new I-frames on STOPDT is right** (`cs104/server_session.go:371`), and the deferral is correctly non-blocking: while pending, the session still answers `TESTFR act` and still acknowledges inbound I-frames, so the peer is not starved.
- **The new `updateAckNoOut` guard in the inactive path is safe**: it correctly rejects out-of-window acks (returns `false`, `cs104/server_session.go:327`), so the added leniency opens no window-violation hole.
- Docs shipped with the fix, the quoted defaults are correct, no public API signatures changed, and the new tests are real end-to-end socket tests rather than mocks.

---

## Problems

### 1. HIGH — `pendingStopDt` is never cleared by STARTDT → spurious STOPDT con

`cs104/server_session.go:185, 215, 372, 375` are the *only* writes to `pendingStopDt`. The `uStartDtActive` case (`cs104/server_session.go:356-358`) sets `isActive = true` but leaves `pendingStopDt` set.

Reachable sequence:

1. Master STARTDT → outstation sends I-frames, nothing acked.
2. Master sends STOPDT act → `isActive=false`, `pendingStopDt=true`, con withheld.
3. Master sends STARTDT act again (normal in the redundant-connection switchover this commit is motivated by) → outstation replies STARTDT con, `isActive=true`.
4. Master sends any I-frame (an interrogation, say). Its `rcvSN` advances `ackNoSend` to `seqNoSend` → `confirmStopDtIfDone()` fires → **STOPDT con emitted immediately after STARTDT con.**

On this library's own client the consequence is concrete: `cs104/client.go:457-460` handles `uStopDtConfirm` by setting `isActive = inactive` and firing `onDeactivated`. Data flow silently stops on a connection the master believes it just started. This is not a cosmetic frame-ordering nit — it de-activates a live session.

**Remediation:** clear `pendingStopDt = false` in the `uStartDtActive` case. Add a regression test (see finding 7).

### 2. MEDIUM — the deferral is a deviation with a real interop cost, and it is not what the standard requires

No standard text was found mandating "acknowledge outstanding I-frames before STOPDT con". The published STOPDT procedure says the controlling station transmits STOPDT act and the controlled station stops data transfer and confirms — with no precondition on the send window. The commit's motivation (redundancy switchover, `docs/cs104.md:313-318`) is realistic and the *problem* it describes is real, but the remedy changes observable behaviour on the wire.

The cost: a controlling station that sends STOPDT act and then **stops sending S-frames** while awaiting the con no longer gets a clean shutdown. The outstation's t₁ check at `cs104/server_session.go:277-283` fires and the connection is torn down. `docs/cs104.md:319-320` acknowledges this and calls it "the correct fallback", but that reframes a peer-compatibility problem as intended behaviour.

Library-internal interop is fine: `cs104/client.go:385-390` keeps emitting S-frames every `timeoutResolution` (100 ms) while `ackNoRcv != seqNoRcv`, so go-iecp5 client ↔ go-iecp5 server completes the STOPDT well inside t₁=15 s. The risk is entirely with third-party masters, which is where an IEC 104 library's users actually are.

**Remediation (preferred):** send the acknowledging S-frame, then the STOPDT con immediately and unconditioned. Same reliability benefit — the outstation's outstanding data is confirmed as received by the peer — with zero timing change for masters.

**Remediation (alternative):** keep the deferral but gate it behind an explicit `Config` opt-in (e.g. `StopDtConfirmOnLastAck bool`, default `false`), so the standards-behaviour remains the default and the improvement is available to operators who know their peers tolerate it.

### 3. MEDIUM — k=1 is now unsatisfiable, contradicting the declared range

`cs104/config.go:139` evaluates `int(w) > int(k)*2/3`. With `k=1` the bound is `0`, and `w`'s own minimum is `1` — so **no valid configuration exists with k=1**, while `SendUnAckLimitKMin = 1` (`cs104/config.go:40`), the error text at `cs104/config.go:96` (`k not in [1, 32767]`), and `docs/cs104.md:213` all declare `k ∈ [1, 32767]` legal. k=2 likewise forces w=1.

`cs104/configrel_test.go:46-49` encodes `{"k of 1 with w of 1", wantErr: true}` as expected behaviour, so a self-contradiction in the public contract is now locked in by a test. k=1 is a legitimate low-bandwidth / single-outstanding-frame setting, not a typo.

Note: `3*w > 2*k` avoids the integer truncation but yields the same verdict at k=1 — this is a policy question (allow the degenerate case, or narrow the documented range to k ≥ 2), not just an arithmetic fix.

**Remediation:** pick one of (a) exempt the degenerate case `k == 1 && w == 1` from the relationship check, or (b) change `SendUnAckLimitKMin` to 2 and update the range check, error text, `docs/cs104.md:213`, and `cs104/configrel_test.go:46-49` to match. Whichever is chosen, make the four places agree.

### 4. MEDIUM — w ≤ ⅔·k couples two independent directions

`k` is *my* send window (`cs104/server_session.go:254`); `w` is *my* receive window (`cs104/server_session.go:348`). The standard's guidance relates the **peer's** k to **my** w. Enforcing it within a single `Config` assumes a symmetric peer, and the rationale comment at `cs104/config.go:135-138` is *logically wrong* for the asymmetric case: a station with k=1, w=64 is perfectly healthy if the peer has k=64 — the sender stalls at 1 outstanding and the receiver acks it after t₂. That is low throughput, not collapse.

A receive-heavy gateway, or a device profile that pins k=1, can now be forced to inflate k (which also sizes the buffers at `cs104/server.go:119-122` and `cs104/client.go:78-81`) purely to satisfy a heuristic about the other direction.

**Remediation:** downgrade to a warning (surfaced through the logging provider) or an opt-in strictness flag, rather than a hard construction-time error. If it stays a hard error, the comment at `cs104/config.go:135-138` must be corrected to state that it assumes a symmetric peer.

### 5. MEDIUM — on `Server`, the new rejection is completely silent

`Valid()` is called from `cs104/server.go:54`, inside `SetConfig(cfg Config) *Server` — **which returns no error**. On failure the config is discarded and `DefaultConfig()` is used. `ClientOption.SetConfig` (`cs104/clientOption.go:39-46`) does return the error, but still substitutes defaults.

So a previously-working config (e.g. t₁=5 s, t₂=10 s) now produces: no error, no log, no diagnostic — the station runs with different k/w/t₁/t₂ than the operator configured. `docs/cs104.md:290-292` says these combinations are "*refused up front*", which overstates the behaviour; for `Server.SetConfig` nothing is refused, it is merely discarded. This widens an existing sharp edge to a much larger set of inputs.

**Remediation:** log the rejection through `Clog` at minimum, in both `SetConfig` variants; and correct the wording at `docs/cs104.md:290-292` to say the config falls back to defaults.

### 6. MEDIUM — the fix is server-only; the client half of the same protocol is untouched

The commit's own stated principle ("the controlling station is still owed data") applies symmetrically, but `cs104/client.go` was not changed:

- `cs104/client.go:651-654` (`SendStopDt`) arms a t₁ timer and tears the connection down if no con arrives within t₁. That is a *new* failure mode when talking to a peer outstation that defers and a peer that stops acking.
- The client has no equivalent "outstanding I-frames are confirmed before I stop sending" guarantee of its own.

Asymmetric fixes in a protocol library tend to surface as the two endpoints disagreeing about what STOPDT means.

**Remediation:** decide and document whether the client needs the mirrored behaviour, or state explicitly in `docs/cs104.md` that the guarantee is outstation-side only.

### 7. MEDIUM — untested new code

`cs104/stopdt_test.go` covers the happy paths well, but:

- **The new `!isActive` I-frame branch (`cs104/server_session.go:325-329`) has zero coverage.** It is the only way an I-frame can complete a pending STOPDT, and it is the exact path implicated in finding 1.
- No test for STOPDT-then-STARTDT (finding 1).
- No test for the documented fallback — "t₁ expires and the connection closes" (`docs/cs104.md:319-320`) is asserted in prose only.
- No test for a repeated STOPDT act while pending (which would produce a duplicate STOPDT con).
- No test for t₁=1 s: STOPDT with outstanding data and a silent master (would also pin the "connection closes" claim).

**Remediation:** add the four cases above to `cs104/stopdt_test.go`.

### 8. LOW — the ticker call to `confirmStopDtIfDone` is unreachable in a consistent state

`cs104/server_session.go:293` sits in the `checkTicker` branch. `ackNoSend` only advances inside `updateAckNoOut`, called from exactly three places:

- the S-frame case (`cs104/server_session.go:316`, which calls `confirmStopDtIfDone` at `:320`),
- the inactive I-frame case (`cs104/server_session.go:327`, which calls it at `:328`),
- the active I-frame case (`cs104/server_session.go:333`, which does **not**).

The active case is only reachable with `isActive == true`, and `pendingStopDt` can only be set while `isActive == false`. So the ticker call can only ever fire through the state hole in finding 1. As a backstop it is defensible; as written it is the delivery mechanism for a bug, and it reads as intentional coverage of a case that cannot occur.

**Remediation:** once finding 1 is fixed, either drop the ticker call or add a comment stating it is a defensive backstop for a state the machine cannot reach.

### 9. LOW — documentation gaps and one inaccuracy

- `cs104/config.go:126-130` describes t₁ solely as "*how long a sender waits for that acknowledgement*". In the standard, t₁ is primarily the STARTDT/STOPDT/TESTFR con timeout, and the k-window timeout is a distinct use. This library overloads one field for both, which is precisely why t₂ < t₁ is needed here — that reasoning should be stated, or a reader implementing to the standard will be confused.
- `docs/cs104.md:308-320` does not state the new **operational requirement it places on the peer**: a controlling station must keep sending S-frames *after* sending STOPDT act, or the connection is dropped. That is the single most important thing for an integrator to know, and it is absent.
- Nothing says the deferral is an implementation choice that third-party masters may not tolerate.
- `SKILL.md` (the "condensed build guide") and `README.md` were not updated, though `SKILL.md:371` is exactly where interop expectations (`k=12, w=8, t1=15s, t2=10s, t3=20s`) are listed. `SKILL.md:336` discusses STOPDT state without the new ordering.
- `docs/cs104.md:292` — "*the two relationships between them*" is vague; one constraint is within-direction and the other is cross-direction (see finding 4).
- `docs/cs104.md:219` ("Both peers should use compatible k/w/t values") is now materially stronger than stated.

**Remediation:** update `cs104/config.go:126-130`, `docs/cs104.md:219, 292, 308-320`, and `SKILL.md:336, 371`.

### 10. LOW — test robustness

- `cs104/stopdt_test.go:70` — `time.Sleep(200 * time.Millisecond)` after `ListenAndServer` instead of waiting on listener readiness. A slow CI machine can fail the first connect.
- The "nothing may be confirmed yet" assertion at `cs104/stopdt_test.go:112-114` passes for the wrong reason if the session happens to emit a `TESTFR act` in that window; it compares against the con bytes rather than asserting *nothing* arrived.
- `readFrame` is now a package-level helper at `cs104/stopdt_test.go:34`, while `cs104/apcistate_test.go` keeps its own local `read` closure — shared test surface with a duplicate idiom one file over.

**Remediation:** wait on a readiness signal; assert no frame at all in the negative case; reconcile the two read helpers.

---

## Standards conformance summary

| Item | Verdict |
|---|---|
| t₂ < t₁ enforced | **Conforms** — genuine standard requirement, matches defaults and the `cs101` precedent |
| w ≤ ⅔·k enforced | **Directionally conforms** (the standard states it as a *recommendation*); enforced as a hard error on a same-station k/w pair, which the standard does not require |
| STOPDT stops new I-frames immediately | **Conforms** |
| STOPDT con withheld until all I-frames acked | **Deviation** — not found to be mandated; defensible, but it changes wire timing and is not covered by the spec |
| Withholding the con while still servicing TESTFR and inbound S-frames | **Conforms** |
| `k` range `[1, 32767]` still achievable | **Non-conformant with own contract** — k=1 is unreachable (finding 3) |

---

## Action plan

Ordered by risk. Each item is independently shippable; items 1–3 should land together.

| # | Priority | Action | Touches | Finding |
|---|---|---|---|---|
| 1 | HIGH | Clear `pendingStopDt` on STARTDT; add STOPDT→STARTDT regression test | `cs104/server_session.go:356-358`, `cs104/stopdt_test.go` | 1, 7 |
| 2 | MEDIUM | Reconsider the deferral: acknowledge with an S-frame *then* confirm unconditionally, **or** gate the deferral behind a `Config` opt-in defaulting to off | `cs104/server_session.go:362-379`, `cs104/config.go`, `cs104/server_session.go:211-217` | 2 |
| 3 | MEDIUM | Reconcile k=1 across the range check, error text, docs, and test — allow the degenerate case or narrow the range to k ≥ 2 | `cs104/config.go:40, 96, 139`, `docs/cs104.md:213`, `cs104/configrel_test.go:46-49` | 3 |
| 4 | MEDIUM | Decide whether w ≤ ⅔·k is a hard error or a warning; if kept hard, correct the rationale comment to state the symmetric-peer assumption | `cs104/config.go:135-142` | 4 |
| 5 | MEDIUM | Make the `SetConfig` rejection observable (log it in both variants); correct "refused up front" | `cs104/server.go:53-60`, `cs104/clientOption.go:39-46`, `docs/cs104.md:290-292` | 5 |
| 6 | MEDIUM | Decide and document whether the client needs the mirrored STOPDT guarantee | `cs104/client.go:457-460, 651-654`, `docs/cs104.md` | 6 |
| 7 | MEDIUM | Add tests: STOPDT→STARTDT, inactive-I-frame ack path, t₁ expiry while pending, duplicate STOPDT act | `cs104/stopdt_test.go` | 7, 1 |
| 8 | LOW | Drop or annotate the unreachable ticker call to `confirmStopDtIfDone` | `cs104/server_session.go:293` | 8 |
| 9 | LOW | Documentation pass: t₁ dual role, the peer S-frame requirement, deviation caveat, `SKILL.md`/`README.md` | `cs104/config.go:126-130`, `docs/cs104.md:219, 292, 308-320`, `SKILL.md:336, 371` | 9 |
| 10 | LOW | Test robustness: readiness signal instead of `Sleep`, assert nothing-arrived in the negative case, reconcile read helpers | `cs104/stopdt_test.go:34, 70, 112-114`, `cs104/apcistate_test.go` | 10 |

### Sequencing notes

- Item 1 is independent of item 2 and should land regardless of how the deferral question is settled.
- If item 2 chooses the opt-in route, items 3–6 all touch `cs104/config.go`; batching them avoids conflicting edits.
- Item 3 changes an expected value in `cs104/configrel_test.go`, so it must land with its test edit in the same commit or the suite goes red.
- Items 9–10 are documentation and test hygiene; safe to defer past a release.

---

## Resolution (2026-09-29)

| # | Outcome |
|---|---|
| 1 | **Fixed.** STARTDT act clears `pendingStopDt`; `TestStartDtCancelsPendingStopDt` fails without the fix. |
| 2 | **Rejected — the finding is wrong.** Withholding STOPDT con until the sent I-frames are acknowledged *is* the IEC 60870-5-104 §5.3 procedure and is checked by the IEC 60870-5-604 conformance tests; lib60870 does the same (`M_CON_STATE_UNCONFIRMED_STOPPED`). Kept, plus the other half of the procedure: on STOPDT act the outstation now S-frame-acknowledges any received I-frames it had not acknowledged yet. The docs now cite the standard. |
| 3, 4 | **Fixed together.** w ≤ ⅔·k is no longer a `Valid()` error (it is a recommendation, and it relates the peer's k to our w). `Config.flowControlAdvice()` computes it without truncation and a warning is logged when the server listens / client starts. k=1 is valid again. |
| 5 | **Fixed.** `Server.SetConfig` logs the rejection and keeps it; `ListenAndServer` logs it again at start. Doc comments and `docs/cs104.md` describe the fallback per entry point. |
| 6 | **Documented** as outstation-side only; the client already acknowledges within 100 ms and its t₁ on STOPDT con is standard. |
| 7 | **Done.** Tests for STOPDT→STARTDT, I-frame ack path, repeated STOPDT, and t₁ expiry while pending. |
| 8 | **Done.** The unreachable ticker call was removed. |
| 9 | **Done.** `cs104/config.go` (t₁ dual role), `docs/cs104.md`, `SKILL.md`. |
| 10 | **Done.** Shared `startServer` waits for the listener; negative checks assert no frame arrived; `apcistate_test.go` uses `readFrame`. |
