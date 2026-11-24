# Changelog

What an operator sees change when they upgrade this driver, newest release
first. `Version` in `Doc()` is the newest heading below.

## 0.1.0 - unreleased

### Added

- `type: prusalink` selects this driver on a connector. It dials an Original
  Prusa MK3S, MK3S+, MK3 or MK2.5S running PrusaLink on a Raspberry Pi.
- The settings `timeout` and `poll_interval`.
- The endpoint options `api_key`, `username`, `password` and `unit_uid`.
- The printer models `MK3S`, `MK3`, `MK2.5S` and `MK2.5` in the device-model
  vocabulary.
- The capabilities `core.command.send_state_info`, `core.command.pause_print`,
  `core.command.resume_print`, `core.command.stop_print`,
  `core.command.start_print`, `core.command.send_info`,
  `core.command.send_file_info`, `core.feature.file_upload`,
  `core.command.delete_file` and `core.command.delete_folder`.
- The refusal codes `NO_JOB` (pause, resume or stop with no print) and
  `NOT_NOW` (PrusaLink refused the command in the print's state).
