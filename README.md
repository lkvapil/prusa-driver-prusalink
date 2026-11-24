# prusalink

A printer driver for [Prusa Connect](https://github.com/prusa3d/prusa-connect)
for the Original Prusa MK3S and MK3S+ with a Raspberry Pi Zero 2 W running
PrusaLink.

| printer | how the Raspberry Pi connects | status |
| --- | --- | --- |
| MK3S, MK3S+ (Einsy RAMBo) | on the GPIO header, inside the printer | the main target, to be tested first |
| MK3 (Einsy RAMBo) | on the GPIO header | should work, not tested |
| MK2.5S (MiniRambo) | over USB, beside the printer | should work, not tested |

The driver speaks only to PrusaLink over the network, and PrusaLink has the
same API on all of these printers. How the Raspberry Pi reaches the printer is
PrusaLink's part, so the driver is the same for each of them.

**Status: tested only against a fake PrusaLink, not yet on a real printer.**

**A bridge until PrusaLink speaks the WebSocket protocol.** Prusa works on a
`websockets` branch of PrusaLink that will let these printers join Prusa
Connect the way an MK4S does, with no driver
([prusa3d/prusa-connect#9](https://github.com/prusa3d/prusa-connect/issues/9)).
It is not released yet. This driver works with the released PrusaLink 0.8.1.
Once the WebSocket update ships, use that instead.
The fake follows the published PrusaLink API, and the driver passes the sdk's
conformance suite and a run in a real Prusa Connect server against that fake.
A test on a real MK3S with a Raspberry Pi comes next.

These printers have a RAMBo board, which cannot speak to Prusa Connect. A
Raspberry Pi Zero 2 W on the GPIO header of the Einsy RAMBo runs
[PrusaLink](https://github.com/prusa3d/Prusa-Link), which drives the printer
over its serial line and serves an HTTP API. This driver reads that API. The
server then shows the MK3S beside an MK4S, at the same state paths.

## What it does

| | |
| --- | --- |
| state | `/state` with PrusaLink's words: IDLE, READY, BUSY, PRINTING, PAUSED, FINISHED, STOPPED, ERROR, ATTENTION |
| readings | nozzle and bed temperature and target, X, Y and Z, speed, flow, print fan and hotend fan, nozzle diameter |
| print | `/job/id`, `/job/name`, progress, time printing, time remaining, and the outcome when it ends |
| commands | pause, resume and stop the print, and start a print of a file on the printer |
| files | the storages, a folder listing, what the slicer wrote about a file, sending a file, deleting a file or a folder |
| sign-in | an API key, or the PrusaLink user and password over HTTP Digest |

PrusaLink's API has no request to set a temperature or to make an empty
folder, so the driver offers neither.

The paths are the ones the xBuddy dialect writes for an MK4S:
`/tools/0/nozzle_temp`, `/temperature/bed`, `/axis/z` and so on. A client reads
both printers the same way.

## Build a server with it

A driver is compiled into the server. `build.yaml` names this module. Run this
from a prusa-connect checkout beside this directory:

```bash
make compose-build BUILD_FILE=../prusa-connect-prusalink/build.yaml
```

`build-minimal.yaml` builds the same server without the web app, which is
quicker when you only want to check the driver:

```text
$ make compose-build BUILD_FILE=../prusa-connect-prusalink/build-minimal.yaml
Building with tags: minimal
Wrote .../prusa-connect/bin/server-prusalink-minimal
```

## Configuration

Name the connector in the server's configuration file:

```yaml
core:
  connectors:
    prusalink:
      type: prusalink
      config:
        poll_interval: 2s
```

| key | type | default | what it controls |
| --- | --- | --- | --- |
| `timeout` | duration | `10s` | How long one request to the printer may take before it is abandoned. |
| `poll_interval` | duration | `2s` | How often the printer's status is read. |

Then tell the server which printer to dial. The address is not in the file: the
server keeps its own list, so a printer is added without a restart.

```bash
curl -X PUT http://127.0.0.1:8080/api/v1/connector-endpoints/mk3s \
  -H "If-None-Match: *" -H "Content-Type: application/json" \
  -d '{"connector": "prusalink", "address": "192.168.1.60", "enabled": true,
       "options": {"username": "maker", "password": "the password from the printer menu"}}'
```

| endpoint option | what it is |
| --- | --- |
| `api_key` | an API key set in PrusaLink, sent as `X-Api-Key` |
| `username`, `password` | the PrusaLink user for HTTP Digest. The default user is `maker`, and the printer shows the password in its menu |
| `unit_uid` | the stable identity. Empty uses the serial number the printer reports |

What the server printed when it dialled the fake printer below:

```text
level=INFO msg="connector dials the endpoints named for it" connector=prusalink type=com.github.aux.lkvapil.prusa-driver-prusalink/prusalink
level=INFO msg="a printer was admitted" model=MK3S serial=CZPX0000X004XK00000 unit_uid=prusalink:CZPX0000X004XK00000 capabilities=4 epoch=1
level=INFO msg="a dialled printer arrived" endpoint=mk3s-test connector=prusalink model=MK3S
```

Where another module has registered the same bare name, write the qualified
type instead: `com.github.aux.lkvapil.prusa-driver-prusalink/prusalink`.

## Try it without a printer

`cmd/prusalinksim` runs a fake PrusaLink with the fields of the published
OpenAPI file:

```bash
go run ./cmd/prusalinksim -user maker -password s3cret -print-after 20s
```

It prints its address. Add it as an endpoint as shown above.

## Tests

```bash
go test -race ./...
```

`TestContract` runs the published conformance suite of the sdk against the fake
printer, including the file half. The other tests follow one print from start
to stop, sign in over Digest, send a file and check its length on the printer,
and end a session when the printer stops answering.

What has not been checked yet: a real MK3S with a Raspberry Pi. The fake
printer follows the published API, and a real PrusaLink may differ in a field
the API file does not describe.

## Status

Version 0.1.0, experimental. It is built against the sdk directory of a
prusa-connect checkout, because the sdk module has no published version yet.
