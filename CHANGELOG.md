# Changelog

## [0.1.3](https://github.com/kacxx/jotaside/compare/v0.1.2...v0.1.3) (2026-11-06)


### Features

* mark unfinished jots with a last-checked date ([#27](https://github.com/kacxx/jotaside/issues/27)) ([e081ed1](https://github.com/kacxx/jotaside/commit/e081ed1d923843c8e9747001a9b4aaed00995b69))

## [0.1.2](https://github.com/kacxx/jotaside/compare/v0.1.1...v0.1.2) (2026-11-01)


### Features

* install on macOS with brew install kacxx/tap/jotaside ([#21](https://github.com/kacxx/jotaside/issues/21)) ([ff88a56](https://github.com/kacxx/jotaside/commit/ff88a56fbc6b1eebe56125b75d6e0c6449128b3e))
* show the first and last records of a long session in activity ([#25](https://github.com/kacxx/jotaside/issues/25)) ([43392f9](https://github.com/kacxx/jotaside/commit/43392f95df21d183e64d57d29cb12ded8ad84ae5))


### Bug Fixes

* ship third-party license notices in the release archives ([#24](https://github.com/kacxx/jotaside/issues/24)) ([6b008a6](https://github.com/kacxx/jotaside/commit/6b008a6af542f674aaa4bff8aca35898bd2aef50))

## [0.1.1](https://github.com/kacxx/jotaside/compare/v0.1.0...v0.1.1) (2026-10-25)


### Bug Fixes

* save a repeated jot once and confirm it as already jotted ([#16](https://github.com/kacxx/jotaside/issues/16)) ([efb387a](https://github.com/kacxx/jotaside/commit/efb387acbb2e8d6040bc1310caafe3c3c710a1ca))

## 0.1.0 (2026-10-24)


### ⚠ BREAKING CHANGES

* the command, Go module, plugin, skill and MCP server are renamed from aside to jotaside. The data directory and JOT_* variables are unchanged. Install jotaside and run `jotaside setup <agent>` to replace the old hook; re-add the MCP server and reinstall the plugin under the new name.

### Features

* rename aside to jotaside ([0150a72](https://github.com/kacxx/jotaside/commit/0150a72d7e2451dc5482ecdb2fbf99f9f585ba18))


### Bug Fixes

* clean jot text before trimming and drop activity records left empty by cleaning ([#13](https://github.com/kacxx/jotaside/issues/13)) ([96fe877](https://github.com/kacxx/jotaside/commit/96fe877d7c23cebdb6beaa73cfeb017d60c152d6))

## aside-jot 0.1.0 to 0.7.0, before the rename

jotaside was renamed from aside-jot (github.com/kacxx/aside-jot, versions 0.1.0
to 0.7.0, with the `aside` command). This changelog starts at the rename;
earlier history is in git and in that repo's releases.
