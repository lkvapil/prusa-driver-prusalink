# Changelog

What an operator sees change when they upgrade this driver, newest release
first. `Version` in `doc()` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a configuration key, a capability,
a refusal code, what the printer now does. Name the key and the code, never a
function or a file.

## 0.1.0 - unreleased

### Added

- `type: prusalink` selects this driver on a connector.
- The setting `timeout` bounds one request to the printer.
- The capabilities `core.command.send_state_info` and
  `core.command.set_nozzle_temperature`.
