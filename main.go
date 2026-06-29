package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"
)

// pauseState allows workers to block between jobs until resumed.
type pauseState struct {
	mu     sync.Mutex
	paused bool
	ch     chan struct{} // closed when transitioning from paused → running
}

func newPauseState() *pauseState {
	return &pauseState{ch: make(chan struct{})}
}

func (p *pauseState) waitIfPaused() {
	for {
		p.mu.Lock()
		if !p.paused {
			p.mu.Unlock()
			return
		}
		ch := p.ch
		p.mu.Unlock()
		<-ch
	}
}

func (p *pauseState) toggle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused {
		p.paused = false
		old := p.ch
		p.ch = make(chan struct{})
		close(old)
		fmt.Println("\nResumed.")
	} else {
		p.paused = true
		fmt.Println("\nPausing after current job completes. Send SIGUSR1 again to resume.")
	}
}

type jobResult struct {
	path         string
	outputPath   string
	originalSize int64
	codec        string
	saved        int64
	err          error
}

type moveJob struct {
	candidate Candidate
	tmpPath   string
	jobNum    int
}

func main() {
	dir := flag.String("dir", "/mnt/Media", "root media directory (used when --plex-url is not set)")
	plexURL := flag.String("plex-url", "", "Plex server URL (e.g. http://localhost:32400); skips filesystem scan")
	plexToken := flag.String("plex-token", "", "Plex API token (required with --plex-url)")
	plexInsecure := flag.Bool("plex-insecure", false, "skip TLS certificate verification for Plex server")
	minSizeGB := flag.Float64("min-size", 20, "minimum file size in GB to target")
	report := flag.Bool("report", false, "print a table of all large files (including H.265) and exit")
	reportCSV := flag.String("report-csv", "", "write report to a CSV file instead of (or in addition to) stdout")
	replace := flag.Bool("replace", false, "delete original after successful transcode (default: keep original alongside .h265.mkv output)")
	dryRun := flag.Bool("dry-run", false, "list candidates without transcoding")
	jobs := flag.Int("jobs", 1, "number of parallel transcode jobs")
	vaapiDevice := flag.String("vaapi-device", "", "force VAAPI encoder with this device path (overrides saved config)")
	detectGPU := flag.Bool("detect-gpu", false, "scan for GPU encoders, save result, and exit")
	qp := flag.Int("qp", 24, "H.265 quantization parameter (lower = better quality, larger files)")
	fileFlag := flag.String("file", "", "transcode a single file instead of scanning")
	tempDir := flag.String("temp-dir", "", "write temp files here during transcoding, then move to destination (useful when output is on a NAS)")
	history := flag.Bool("history", false, "print transcoding history and exit")
	flag.Parse()

	if *history {
		if err := PrintHistory(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *detectGPU {
		enc, notes := DetectEncoder()
		for _, n := range notes {
			fmt.Println(" ", n)
		}
		fmt.Printf("Selected: %s\n", enc.Description())
		if err := SaveEncoderConfig(enc); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save encoder config: %v\n", err)
		} else {
			fmt.Println("Saved to ~/.config/media-convert/encoder.json")
		}
		return
	}

	// Resolve encoder: explicit flag > saved config > auto-detect on first run.
	var enc EncoderConfig
	if *vaapiDevice != "" {
		enc = EncoderConfig{Type: "vaapi", Device: *vaapiDevice}
	} else {
		enc = LoadEncoderConfig()
		if enc.Type == "" {
			var notes []string
			enc, notes = DetectEncoder()
			fmt.Println("No encoder configured — detecting GPU:")
			for _, n := range notes {
				fmt.Println(" ", n)
			}
			fmt.Printf("Selected: %s\n", enc.Description())
			if err := SaveEncoderConfig(enc); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not save encoder config: %v\n", err)
			}
		}
	}

	var flagErrs []string
	if *jobs < 1 {
		flagErrs = append(flagErrs, "--jobs must be at least 1")
	}
	if *qp < 0 || *qp > 51 {
		flagErrs = append(flagErrs, "--qp must be between 0 and 51")
	}
	if *minSizeGB <= 0 {
		flagErrs = append(flagErrs, "--min-size must be greater than 0")
	}
	if *fileFlag == "" && *plexURL == "" {
		if _, err := os.Stat(*dir); err != nil {
			flagErrs = append(flagErrs, fmt.Sprintf("--dir %q: %v", *dir, err))
		}
	}
	if *tempDir != "" {
		if info, err := os.Stat(*tempDir); err != nil {
			flagErrs = append(flagErrs, fmt.Sprintf("--temp-dir %q: %v", *tempDir, err))
		} else if !info.IsDir() {
			flagErrs = append(flagErrs, fmt.Sprintf("--temp-dir %q: not a directory", *tempDir))
		}
	}
	if *plexURL != "" {
		if _, err := url.ParseRequestURI(*plexURL); err != nil {
			flagErrs = append(flagErrs, fmt.Sprintf("--plex-url %q is not a valid URL", *plexURL))
		}
	}
	if enc.Type == "vaapi" {
		if _, err := os.Stat(enc.Device); err != nil {
			flagErrs = append(flagErrs, fmt.Sprintf("VAAPI device %q: %v", enc.Device, err))
		}
	}
	if len(flagErrs) > 0 {
		for _, e := range flagErrs {
			fmt.Fprintf(os.Stderr, "error: %s\n", e)
		}
		os.Exit(1)
	}

	if *plexURL != "" {
		if *plexToken != "" {
			// Explicit token provided — save it for future runs.
			if LoadToken() != *plexToken {
				if err := SaveToken(*plexToken); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not save token: %v\n", err)
				}
			}
		} else if saved := LoadToken(); saved != "" {
			*plexToken = saved
		} else {
			fmt.Println("No --plex-token provided; signing in to plex.tv to obtain one.")
			tok, err := PlexLogin()
			if err != nil {
				fmt.Fprintf(os.Stderr, "plex login failed: %v\n", err)
				os.Exit(1)
			}
			*plexToken = tok
			if err := SaveToken(tok); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not save token: %v\n", err)
			} else {
				fmt.Println("Token saved to ~/.config/media-convert/token")
			}
		}
	}

	var candidates []Candidate

	if *fileFlag != "" {
		info, err := os.Stat(*fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		codec, err := getVideoCodec(*fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not probe file: %v\n", err)
			os.Exit(1)
		}
		if isHEVC(codec) {
			fmt.Println("File is already H.265 — nothing to do.")
			return
		}
		candidates = []Candidate{{Path: *fileFlag, Size: info.Size(), Codec: codec}}
	} else {
		minBytes := int64(*minSizeGB * 1024 * 1024 * 1024)
		skipHEVC := !(*report || *reportCSV != "")
		var err error
		if *plexURL != "" {
			fmt.Printf("Querying Plex at %s for video files >= %.0f GB...\n", *plexURL, *minSizeGB)
			candidates, err = FindCandidatesFromPlex(*plexURL, *plexToken, minBytes, *plexInsecure, skipHEVC)
		} else {
			fmt.Printf("Scanning %s for video files >= %.0f GB...\n", *dir, *minSizeGB)
			candidates, err = FindCandidates(*dir, minBytes, skipHEVC)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
			os.Exit(1)
		}
	}

	if *report || *reportCSV != "" {
		if err := writeReport(candidates, *report, *reportCSV); err != nil {
			fmt.Fprintf(os.Stderr, "report error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(candidates) == 0 {
		fmt.Println("No candidates found.")
		return
	}

	total := len(candidates)
	fmt.Printf("\nFound %d candidate(s):\n", total)
	var totalBytes int64
	for i, c := range candidates {
		fmt.Printf("  %d. [%s] %s\n", i+1, formatSize(c.Size), c.Path)
		totalBytes += c.Size
	}
	if total > 1 {
		fmt.Printf("\nTotal size: %s\n", formatSize(totalBytes))
	}

	if *dryRun {
		return
	}

	fmt.Print("\nProceed with transcoding? [y/N] ")
	sc := bufio.NewScanner(os.Stdin)
	sc.Scan()
	if strings.ToLower(strings.TrimSpace(sc.Text())) != "y" {
		fmt.Println("Aborted.")
		return
	}

	pause := newPauseState()

	// Ctrl+C: finish current job(s) then exit.
	stopCh := make(chan struct{})
	sigIntCh := make(chan os.Signal, 1)
	signal.Notify(sigIntCh, syscall.SIGINT)
	go func() {
		<-sigIntCh
		fmt.Println("\nStopping after current job(s) finish...")
		close(stopCh)
		signal.Stop(sigIntCh)
	}()

	// SIGUSR1: toggle pause between jobs.
	sigUsrCh := make(chan os.Signal, 1)
	signal.Notify(sigUsrCh, syscall.SIGUSR1)
	go func() {
		for range sigUsrCh {
			pause.toggle()
		}
	}()

	fmt.Printf("PID %d — send SIGUSR1 to pause/resume, Ctrl+C to stop after current job.\n\n", os.Getpid())

	workCh := make(chan Candidate, total)
	for _, c := range candidates {
		workCh <- c
	}
	close(workCh)

	moveCh := make(chan moveJob, total)
	resultCh := make(chan jobResult, total)
	var counter atomic.Int32
	var encodeWg sync.WaitGroup

	for j := 0; j < *jobs; j++ {
		encodeWg.Add(1)
		go func() {
			defer encodeWg.Done()
			for c := range workCh {
				select {
				case <-stopCh:
					return
				default:
				}

				pause.waitIfPaused()

				select {
				case <-stopCh:
					return
				default:
				}

				n := int(counter.Add(1))
				fmt.Printf("[%d/%d] Encoding: %s\n", n, total, c.Path)
				tmpPath, err := EncodeAndVerify(c.Path, enc, *qp, *tempDir)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[%d/%d] FAILED: %s: %v\n", n, total, c.Path, err)
					resultCh <- jobResult{path: c.Path, originalSize: c.Size, codec: c.Codec, err: err}
					continue
				}
				moveCh <- moveJob{candidate: c, tmpPath: tmpPath, jobNum: n}
			}
		}()
	}

	// Close moveCh once all encode workers are done.
	go func() {
		encodeWg.Wait()
		close(moveCh)
	}()

	// Single move goroutine: copy temp → final destination while next encode runs.
	go func() {
		for job := range moveCh {
			c := job.candidate
			n := job.jobNum
			if *tempDir != "" {
				fmt.Printf("[%d/%d] Moving to destination: %s\n", n, total, c.Path)
			}
			saved, err := CommitTemp(c.Path, job.tmpPath, *replace)
			result := jobResult{path: c.Path, originalSize: c.Size, codec: c.Codec, saved: saved, err: err}
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%d/%d] FAILED (move): %s: %v\n", n, total, c.Path, err)
			} else {
				result.outputPath = FinalPath(c.Path, *replace)
				fmt.Printf("[%d/%d] Done: %s (saved %s)\n", n, total, c.Path, formatSize(saved))
			}
			resultCh <- result
		}
		close(resultCh)
	}()

	var totalSaved int64
	var errCount, doneCount int
	run := RunRecord{Timestamp: time.Now()}
	for r := range resultCh {
		doneCount++
		fr := FileRecord{Path: r.path, OutputPath: r.outputPath, OriginalSize: r.originalSize, Codec: r.codec}
		if r.err != nil {
			errCount++
			fr.Error = r.err.Error()
		} else {
			fr.SavedBytes = r.saved
			if r.saved > 0 {
				totalSaved += r.saved
			}
		}
		run.Files = append(run.Files, fr)
	}
	run.Converted = doneCount - errCount
	run.Failed = errCount
	run.TotalSaved = totalSaved

	skipped := total - doneCount
	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("Converted: %d  Failed: %d  Skipped: %d\n", run.Converted, errCount, skipped)
	fmt.Printf("Space reclaimed: %s\n", formatSize(totalSaved))

	if run.Converted > 0 || run.Failed > 0 {
		if err := AppendRun(run); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save history: %v\n", err)
		}
	}

	if *plexURL != "" && run.Converted > 0 {
		var outputPaths []string
		for _, fr := range run.Files {
			if fr.OutputPath != "" {
				outputPaths = append(outputPaths, fr.OutputPath)
			}
		}
		if len(outputPaths) > 0 {
			fmt.Println("\nRefreshing Plex library...")
			if err := RefreshPlexDirs(*plexURL, *plexToken, *plexInsecure, outputPaths); err != nil {
				fmt.Fprintf(os.Stderr, "warning: plex refresh failed: %v\n", err)
			}
		}
	}
}

func writeReport(candidates []Candidate, table bool, csvPath string) error {
	if table {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SIZE\tCODEC\tPATH")
		fmt.Fprintln(w, "----\t-----\t----")
		var total int64
		for _, c := range candidates {
			fmt.Fprintf(w, "%s\t%s\t%s\n", formatSize(c.Size), c.Codec, c.Path)
			total += c.Size
		}
		fmt.Fprintf(w, "\nTotal: %s\t%d file(s)\t\n", formatSize(total), len(candidates))
		if err := w.Flush(); err != nil {
			return err
		}
	}

	if csvPath != "" {
		f, err := os.Create(csvPath)
		if err != nil {
			return err
		}
		defer f.Close()
		w := csv.NewWriter(f)
		w.Write([]string{"Path", "Size", "Size (bytes)", "Codec"})
		for _, c := range candidates {
			w.Write([]string{c.Path, formatSize(c.Size), fmt.Sprintf("%d", c.Size), c.Codec})
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		fmt.Printf("Report written to %s (%d file(s))\n", csvPath, len(candidates))
	}

	return nil
}

func formatSize(bytes int64) string {
	const (
		GB = 1024 * 1024 * 1024
		MB = 1024 * 1024
	)
	if bytes >= GB {
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	}
	return fmt.Sprintf("%.0f MB", float64(bytes)/float64(MB))
}
