# Agent Instructions

This document contains instructions for AI agents working on this codebase.

## Code Quality Requirements

### Cyclomatic Complexity

All functions in the library code must have a cyclomatic complexity of 5 or less. The `make gocyclo` command enforces this requirement.

**To check:**
```bash
make gocyclo
```

### Linter Compliance

All code must pass `golangci-lint` checks:

```bash
make golangci
```

## Testing

```bash
make test
```

## Building

```bash
make build
```
