# Changelog

All notable changes to this project will be documented in this file.

Please choose versions by [Semantic Versioning](http://semver.org/).

* MAJOR version when you make incompatible API changes,
* MINOR version when you add functionality in a backwards-compatible manner, and
* PATCH version when you make backwards-compatible bug fixes.

## Unreleased

- feat: add pkg/filter decision chain (RepoAllowlistFilter, GoModPresentFilter, GoModParsableFilter, GoBehindFilter, AutoUpdateFilter, SHAUnchangedFilter) with TaskCreationFilter interface and Candidate type
- feat: add pkg/cursor for persisted-memory LoadCursor/SaveCursor with atomic write via temp file + rename
- feat: add pkg/cursorreader adapter bridging Cursor to filter.CursorReader without import cycle

## v0.0.1

- initial scaffold from `go-skeleton`
