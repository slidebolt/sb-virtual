# Git Workflow for sb-virtual

This repository contains the Slidebolt Virtual Device service, which allows creating and managing virtual entities. It produces a standalone binary.

## Dependencies
- **Internal:**
  - `sb-contract`: Core interfaces and shared structures.
  - `sb-domain`: Shared domain models for entities.
  - `sb-logging`: Logging implementation.
  - `sb-logging-sdk`: Logging client interfaces.
  - `sb-messenger-sdk`: Shared messaging interfaces.
  - `sb-runtime`: Core execution environment.
  - `sb-storage-sdk`: Shared storage interfaces.
  - `sb-storage-server`: Storage implementation.
- **External:** 
  - Standard Go library and NATS.

## Build Process
- **Type:** Go Application (Service).
- **Consumption:** Run as the virtual device provider for Slidebolt.
- **Artifacts:** Produces a binary named `sb-virtual`.
- **Command:** `go build -o sb-virtual ./cmd/sb-virtual`
- **Validation:** 
  - Validated through unit tests: `go test -v ./...`
  - Validated by successful compilation of the binary.

## Pre-requisites & Publishing
As a service providing virtual entities, `sb-virtual` must be updated whenever the core domain, logging, or messaging SDKs are changed.

**Before publishing:**
1. Determine current tag: `git tag | sort -V | tail -n 1`
2. Ensure all local tests pass: `go test -v ./...`
3. Ensure the binary builds: `go build -o sb-virtual ./cmd/sb-virtual`

**Publishing Order:**
1. Ensure all internal dependencies are tagged and pushed.
2. Update `sb-virtual/go.mod` to reference the latest tags.
3. Determine next semantic version for `sb-virtual` (e.g., `v1.0.4`).
4. Commit and push the changes to `main`.
5. Tag the repository: `git tag v1.0.4`.
6. Push the tag: `git push origin main v1.0.4`.

## Update Workflow & Verification
1. **Modify:** Update virtual device logic in `virtual/` or `app/`.
2. **Verify Local:**
   - Run `go mod tidy`.
   - Run `go test ./...`.
   - Run `go build -o sb-virtual ./cmd/sb-virtual`.
3. **Commit:** Ensure the commit message clearly describes the virtual device change.
4. **Tag & Push:** (Follow the Publishing Order above).
