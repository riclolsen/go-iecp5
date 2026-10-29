# Interoperability tests

go-iecp5 against independent IEC 60870-5-104 implementations, using the
reference images of [otfabric/iec104-interop](https://github.com/otfabric/iec104-interop)
(MZ Automation lib60870-C, OpenMUC j60870, wendy512/iec104 and cB-Sigma/iec60870,
each in a Docker image with the same command line and JSON documents).

That project owns the references, the fixtures and the container contract; this
package is a *consumer* of it, as the contract intends: it owns its scenarios,
assertions and the list of known gaps. Nothing of the references is vendored
except `testdata/cases.tsv`, a copy of their case table.

## Running

Needs Docker and network access to `ghcr.io` (images are pulled when missing).
The tests are skipped unless asked for, so `go test ./...` stays hermetic:

```sh
IEC104_INTEROP=1 go test ./interop/ -v              # about 3 minutes
IEC104_INTEROP=1 IEC104_INTEROP_ADAPTERS=lib60870,cbsigma go test ./interop/
IEC104_INTEROP=1 IEC104_INTEROP_TAG=v0.3.1 go test ./interop/
```

| Variable | Meaning |
|----------|---------|
| `IEC104_INTEROP` | Set to anything to enable the suite |
| `IEC104_INTEROP_TAG` | Image tag, default `v0.3.1` |
| `IEC104_INTEROP_ADAPTERS` | Comma separated subset of `lib60870,openmuc,wendy512,cbsigma` |
| `IEC104_INTEROP_LOG` | Set to log the in-process client's frames |

The containers publish on `127.0.0.1` only. For the outstation direction the
containers reach the host as `host.docker.internal` (`--add-host ...:host-gateway`),
and the station listens on all interfaces on a free port for the duration of a test.

## What is tested

| Test | go-iecp5 is | Reference is | Checks |
|------|-------------|--------------|--------|
| `TestMaster` | `cs104.Client` | station | every case of `testdata/cases.tsv` |
| `TestMasterInterrogationValues` | `cs104.Client` | station | value and quality of every point against the image's fixture |
| `TestOutstation` | `cs104.Server` (`station_test.go`) | client | every case of `testdata/cases.tsv` |
| `TestOutstationInterrogationValues` | `cs104.Server` | client | the same, as the client decoded it |
| `TestOutstationFileSHA256` | `cs104.Server` + `filetransfer.Sender` | client | SHA-256 of the downloaded files |

A case is an operation (`interrogate`, `command --mode sbo ...`, `file-get`, ...)
with the expected outcome: success, error code, confirmations, termination and
the type/cause of every ASDU received, in order. The same table drives both
directions: the in-process client (`inproc_client_test.go`) builds the result
document of the container contract, so it is compared exactly like a reference
client's output.

Expected values come from the image (`print-fixture`), and what a stack lacks
comes from `print-capabilities`: a case needing a feature the reference does not
declare is skipped, not failed.

`station_test.go` is a controlled station implementing the behaviour in
[FIXTURES.md](https://github.com/otfabric/iec104-interop/blob/main/docs/FIXTURES.md)
with only go-iecp5's public API.

## Known gaps

`testdata/known-gaps.tsv` lists cases that fail today and why. A matching case
that fails is reported as skipped with the reason; one that **passes** fails the
run ("stale entry"), so a fix cannot go unnoticed and the entry gets removed.

Only upstream's own bug is listed now: OpenMUC j60870 1.8.0 cannot decode
C_RD_NA_1 (also in iec104-interop's `known-bugs.tsv`).

These tests found, and the library now fixes, four gaps of go-iecp5:

- time-tagged command types (58..64) were not decoded on reception
  (`asdu.infoObjSize`);
- `filetransfer.Receiver` never sent *call file* after F_FR, and
  `filetransfer.Sender` answered *select file* with F_SR instead of F_FR and
  sent no file checksum, so file transfer worked between the two but not with
  a standard peer; the `Receiver` also delivered a finished file before
  sending the final acknowledgement;
- `asdu.MeasuredValue*CP24/CP56Time2a` refused cause 11/12 and
  `asdu.IntegratedTotals*` refused cause 5, so a station could not send the
  report of a set-point command or answer a read of an integrated total.

## Updating

Bump `defaultTag`, copy `tests/cases.tsv` of that release over
`testdata/cases.tsv` (keep the two header lines), run the suite, and adjust
`testdata/known-gaps.tsv`.
