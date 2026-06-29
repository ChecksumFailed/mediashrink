# media-convert

Scans a media library for large video files and transcodes them to H.265 using AMD VAAPI hardware acceleration. Originals are only deleted after the output is verified.

## Requirements

- Go 1.21+
- `ffmpeg` and `ffprobe` with VAAPI support
- AMD GPU with VAAPI (`/dev/dri/renderD128`)

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

# Target a single file
./media-convert --file /mnt/Media/Movies/BigMovie.mkv

# Lower the size threshold to 10 GB
./media-convert --min-size 10

# Scan a different directory
./media-convert --dir /mnt/OtherMedia
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--dir` | `/mnt/Media` | Root media directory (scans `TV` and `Movies` subdirs) |
| `--file` | — | Transcode a single file instead of scanning |
| `--min-size` | `20` | Size threshold in GB |
| `--dry-run` | off | List candidates without transcoding |
| `--qp` | `24` | H.265 quality (18 = excellent, 24 = good, 28 = smaller files) |
| `--jobs` | `1` | Parallel transcode jobs (VAAPI rarely benefits from >1) |
| `--vaapi-device` | `/dev/dri/renderD128` | VAAPI device path |

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

1. Walks `<dir>/TV` and `<dir>/Movies` for video files at or above `--min-size`
2. Probes each file with `ffprobe` — skips files already encoded as H.265
3. Prints the candidate list and total size, then asks for confirmation
4. Transcodes each file to `.mkv` using `hevc_vaapi` at the given QP
5. Verifies the output codec and that the duration matches the source (within 1%)
6. On success: replaces the original with the transcoded file
7. On failure: deletes the temp file, leaves the original untouched
