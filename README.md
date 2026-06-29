# mediashrink

Scans a media library for large video files and transcodes them to H.265 using hardware acceleration (AMD/Intel VAAPI, NVIDIA NVENC, or software fallback). By default the original is kept alongside the new `.h265.mkv` output; pass `--replace` to delete it after a verified transcode. Supports Plex as an alternative to filesystem scanning for faster candidate discovery.

## Requirements

- Go 1.21+
- `ffmpeg` and `ffprobe` built with the relevant encoder support:
  - AMD/Intel GPU → VAAPI (`hevc_vaapi`)
  - NVIDIA GPU → NVENC (`hevc_nvenc`)
  - No GPU → software (`libx265`, slow)

On first run the encoder is detected automatically and saved to `~/.config/media-convert/encoder.json`.

## Build

```bash
go build -buildvcs=false -o media-convert .
```

## Usage

```bash
# See what would be converted — no changes made
./media-convert --dry-run

# Run for real (prompts for confirmation before starting)
./media-convert

# Replace originals after verified transcode
./media-convert --replace

# Target a single file
./media-convert --file /mnt/Media/Movies/BigMovie.mkv

# Lower the size threshold to 10 GB
./media-convert --min-size 10

# Scan a specific directory
./media-convert --dir /mnt/OtherMedia

# Use Plex instead of walking the filesystem (much faster for large libraries)
./media-convert --plex-url http://localhost:32400 --dry-run
# First run prompts for plex.tv credentials and saves the token automatically.
# Subsequent runs load the token from ~/.config/media-convert/token.

# Report all large files with codec info — no transcoding
./media-convert --report --min-size 10
./media-convert --report-csv report.csv

# View transcoding history
./media-convert --history
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--dir` | `/mnt/Media` | Root media directory to scan |
| `--file` | — | Transcode a single file instead of scanning |
| `--min-size` | `20` | Size threshold in GB |
| `--replace` | off | Delete original after successful transcode |
| `--dry-run` | off | List candidates without transcoding |
| `--report` | off | Print a table of all large files (including H.265) and exit |
| `--report-csv` | — | Write report to a CSV file |
| `--history` | off | Print transcoding history and exit |
| `--qp` | `24` | H.265 quality (18 = excellent, 24 = good, 28 = smaller files) |
| `--jobs` | `1` | Parallel transcode jobs (hardware encoders rarely benefit from >1) |
| `--vaapi-device` | — | Force VAAPI encoder with a specific device path |
| `--detect-gpu` | off | Scan for GPU encoders, save result, and exit |
| `--plex-url` | — | Plex server URL — uses Plex API instead of filesystem scan |
| `--plex-token` | — | Plex API token (prompted and saved automatically if omitted) |
| `--plex-insecure` | off | Skip TLS certificate verification for Plex server |

## Pause / Stop

The PID is printed when transcoding begins:

```
PID 12345 — send SIGUSR1 to pause/resume, Ctrl+C to stop after current job.
```

| Action | Effect |
|---|---|
| `kill -USR1 <pid>` | Pause after current job finishes |
| `kill -USR1 <pid>` (again) | Resume |
| `Ctrl+C` | Finish current job(s), then exit |

## How it works

1. Discovers candidates via filesystem walk (`--dir`) or Plex API (`--plex-url`)
2. Filters to files at or above `--min-size` that are not already H.265
3. Prints the candidate list and total size, then asks for confirmation
4. Transcodes each file to `.mkv` using the detected encoder at the given QP
5. Verifies the output codec and that the duration matches the source (within 1%)
6. On success: writes `<stem>.h265.mkv` (or replaces original if `--replace`)
7. On failure: deletes the temp file, leaves the original untouched
8. Appends results to `~/.config/media-convert/history.json` for later review

## Persistent storage

All state is kept in `~/.config/media-convert/`:

| File | Contents |
|---|---|
| `token` | Saved Plex auth token (0600 permissions) |
| `encoder.json` | Detected GPU encoder config (type + device path) |
| `history.json` | Per-run transcoding records (JSON Lines) |
