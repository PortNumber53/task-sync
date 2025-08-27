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

- Rubric Shell: removed hard-coded `ansible` subdirectory for GOLDEN `held_out_test_clean_up` execution.
  - Change: use `appFolder` as working directory in `internal/process_rubric_shell.go` to match rubric execution context.
  - Impact: avoids incorrect assumptions about repo layout and prevents accidental path-related side effects.

- Docker Volume Pool: fix stray folder creation from grading_setup_script
  - Bugfix: corrected `docker cp` destination missing colon when copying to container (`%s:/tmp/grading_setup.patch`).
  - Pathing: resolve `grading_setup_script` relative to `tasks.local_path` (aka `stepExec.BasePath`) when provided as a relative path.
  - Callers updated to pass basePath to `ApplyGitCleanupAndPatch`.
  - Build verified: `go build ./...`.
