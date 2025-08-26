# Changelog

## 2025-08-26

- Add CLI subcommand `step original` to run rubric_shell steps in Original-only mode.
  - New handler: `cmd/HandleStepOriginal()`
  - Dispatcher wired in `cmd/HandleStep()` (subcommand: `original`)
  - Uses `internal.SetRubricRunModeForCLI("original-only")` to restrict assignments
  - Invokes `internal.ProcessSpecificStep(..., force, golden=false, original=true)`
- Help improvements: `step original --help` now shows dedicated usage via `help.PrintStepOriginalHelp()`
- Fixed accidental duplicate function definitions in `cmd/cmd.go` and cleaned up malformed code blocks
- Build verified: `go build ./...` succeeds
