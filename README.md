# prusalink

A printer driver for prusalink machines, built against the
prusa-connect sdk. `scaffold-driver` wrote this skeleton.

An operator selects it by writing `type: prusalink` under a connector. Where
another module has registered the same bare name, they write the qualified
form instead:

```
com.github.aux.lkvapil.prusa-driver-prusalink/prusalink
```

| file | what it is |
| --- | --- |
| `prusalink.go` | the dialect: the identity exchange, the capability ceiling, the command and the state stream |
| `faults.go` | every code this driver fails with, each documented once |
| `prusalink_test.go` | the published conformance suite, the registration, and the settings |
| `build.yaml` | the build configuration that puts this driver into a server binary |
| `CHANGELOG.md` | what an operator sees change when they upgrade this driver |

## Installation

There is no install step of its own. A driver is compiled into a server binary,
and `build.yaml` is what names it. Run this from a prusa-connect checkout:

```bash
make compose-build BUILD_FILE=../prusa-connect-prusalink/build.yaml
```

Paste what the command printed on your machine under this paragraph, and the
first lines the built binary prints when it starts. A README that quotes real
output is one a reader can check; one that describes the output is one they have
to trust.

Run the tests from this directory:

```bash
go test ./...
```

## Configuration

An operator writes one connector per printer. The address is not a key here:
the server keeps its own list of what to dial and hands this driver one endpoint
per address, so a printer is added without a restart and without an edit to the
configuration file.

```yaml
resources:
  driver:
    prusalink-1:
      type: prusalink
      config:
        timeout: 10s
```

| key | type | default | what it controls |
| --- | --- | --- | --- |
| `timeout` | duration | `10s` | How long one request to the printer may take before it is abandoned. |

The table is the same list `doc()` declares in `prusalink.go`, and
`TestTheDeclaredDefaultIsTheOneTheCodeUses` holds the two together. Nothing here
reads an environment variable, so this table is the whole of what an operator
can set.

## Permissions

A driver takes no permission of its own. It acts for the server, which decides
who may send a command before it reaches this code, and it holds no grant. What
a driver may do to a printer is its capability set instead: `Baseline` in
`prusalink.go` is the ceiling, the host's profile subtracts from it, and what
the printer reports subtracts again. A driver that adds a capability back fails
the conformance suite.

## Failures

`faults.go` holds every code this driver fails with. A failure that reaches a
person or a client is a registered code in this driver's own domain,
`com.github.aux.lkvapil.prusa-driver-prusalink`, with its documentation beside it and the English sentence
kept. A caller branches on the code with `fault.HasCode`, so a reworded sentence
never breaks a caller and a new code always does.

Add a code there and nowhere else.

## Troubleshooting

| what you see | what it means |
| --- | --- |
| `com.github.aux.lkvapil.prusa-driver-prusalink/DEVICE_UNREACHABLE` | the printer did not answer. The `address` parameter names what was dialled |
| `com.github.aux.lkvapil.prusa-driver-prusalink/INVALID_CONFIG` | the connector's `config` block cannot be read. The `key` parameter names the line |
| a command is refused before it is sent | the printer does not have that capability. Compare `Baseline` with what the printer reported |
| the printer goes away and comes back | the state stream ended. This dialect has no renegotiation, so the server reconnects as a new session |
| nothing registers the driver | the server binary was not built from `build.yaml`. A binary built from a configuration that does not name this module has never heard of it |
