# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Purpose

CLI tool (`media-convert`) that scans a media library at `/mnt/Media` (TV and Movies subdirs), finds video files above a configurable size threshold that are not already H.265, and transcodes them using AMD VAAPI hardware acceleration. Originals are deleted only after the output is verified.

## Commands

```bash
# Build (git repo has no upstream, so VCS stamping must be disabled)
go build -buildvcs=false -o media-convert .

# Dry run — lists candidates without touching anything
./media-convert --dry-run

# Transcode a single file (keeps original by default, add --replace to delete it)
./media-convert --file /mnt/Media/Movies/BigMovie.mkv
./media-convert --file /mnt/Media/Movies/BigMovie.mkv --replace

# Full run with non-default options
./media-convert --dir /mnt/Media --min-size 10 --qp 26 --jobs 1 --replace

# Use Plex to get candidate list (faster — skips filesystem walk and ffprobe)
./media-convert --plex-url http://localhost:32400 --plex-token YOUR_TOKEN --dry-run

# Use Plex without a pre-existing token (prompts for plex.tv credentials, saves token)
./media-convert --plex-url http://localhost:32400 --dry-run

# Report: table of all large files with codec info (no transcoding)
./media-convert --report --min-size 10
./media-convert --report-csv report.csv --min-size 10
./media-convert --report --report-csv report.csv  # both at once

# View transcoding history
./media-convert --history
```

## Architecture

Four source files, one external dependency (`golang.org/x/term`):

- **`scan.go`** — `FindCandidates(root, minBytes, skipHEVC)`: walks the given directory recursively, filters by file size and video extension, then runs `ffprobe` to get the codec. Skips HEVC files when `skipHEVC=true` (transcode mode); includes them when `skipHEVC=false` (report mode).
- **`plex.go`** — `FindCandidatesFromPlex(baseURL, token, minBytes, insecure, skipHEVC)`: queries the Plex API for all movie and TV libraries, returns candidates using Plex's indexed codec/size data (no ffprobe needed). `PlexLogin()` prompts for plex.tv credentials and exchanges them for an auth token. `--plex-insecure` skips TLS cert verification.
- **`transcode.go`** — `Transcode(src, vaapiDevice, qp, replace)`: runs `ffmpeg` with `hevc_vaapi`, writes to a `.tmp.mkv` sidecar, verifies codec and duration via `ffprobe` (must be within 1% of source). When `replace=true`, atomically renames to `<stem>.mkv` and removes the original. When `replace=false` (default), writes to `<stem>.h265.mkv` and keeps the original. On any failure the temp file is cleaned up.
- **`main.go`** — parses flags, prints candidate list with sizes, prompts for confirmation, runs a worker pool (`--jobs`) fed from a queue channel so pause/stop checks happen between jobs. Prints per-file results and a final summary. `writeReport` handles `--report` / `--report-csv` output.
- **`config.go`** — manages `~/.config/media-convert/`: `LoadToken`/`SaveToken` for the Plex auth token, `LoadEncoderConfig`/`SaveEncoderConfig`/`DetectEncoder` for GPU encoder detection and persistence, `AppendRun`/`PrintHistory` for the JSON Lines history log.

## Signals

- **SIGUSR1** — toggles pause between jobs (current job finishes, next job waits). The PID is printed at startup. Resume with a second `kill -USR1 <pid>`.
- **Ctrl+C (SIGINT)** — graceful stop: current job(s) finish, no new jobs start, summary is printed.

## Key Behaviours

- Files already encoded as `hevc`/`h265` are skipped during scan.
- Output is always `.mkv` regardless of input container.
- `--qp` (default 24) controls H.265 quality; lower = better quality + larger file.
- VAAPI device defaults to `/dev/dri/renderD128`; override with `--vaapi-device`.
- `--jobs` defaults to 1; VAAPI hardware rarely benefits from more than 1 parallel job.
