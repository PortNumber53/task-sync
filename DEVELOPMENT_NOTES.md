# Development Notes

This document contains useful hints and summaries of fixes for issues that have come up during development.

## Fixing Multi-line Parameter Handling in `step create`

**Date:** 2025-07-04

### Problem

The `step create` command was failing when a multi-line JSON string was passed to the `--settings` flag. The command would fail with an error like `Error: --task, --title, and --settings are required`, indicating that the arguments were not being parsed correctly.

The root cause was that the command-line argument parsing logic in `cmd/cmd.go`'s `HandleStepCreate` function was not correctly handling the multi-line JSON string passed from the shell. Initial attempts to fix this involved complex logic to concatenate all subsequent non-flag arguments into a single string, but this approach was brittle and introduced several other bugs and compilation errors.

### Solution

The final solution was to simplify the argument parsing logic significantly. The key realization was that when a multi-line string is properly quoted in the shell (e.g., using single quotes `'...'`), the shell passes it to the Go application as a single, complete argument.

The fix involved replacing the complex argument-joining logic with a simple assignment:

```go
// In cmd/cmd.go -> HandleStepCreate

case "--settings":
    if i+1 >= len(args) {
        // ... error handling ...
    }
    settings = args[i+1] // Treat the value as a single argument
    i++
```

This simplification made the parsing logic more robust and resolved the issue. The command now correctly accepts complex, multi-line JSON for the `--settings` parameter.

**Key takeaway:** When dealing with command-line arguments from the shell, trust that the shell will handle quoting and pass multi-line strings as a single argument. Avoid overly complex logic to re-assemble arguments in the application code.
