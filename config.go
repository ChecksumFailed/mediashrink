package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type EncoderConfig struct {
	Type   string `json:"type"`             // "vaapi", "nvenc", "software"
	Device string `json:"device,omitempty"` // VAAPI render node path
}

func (e EncoderConfig) Description() string {
	switch e.Type {
	case "vaapi":
		return fmt.Sprintf("VAAPI — AMD/Intel (%s)", e.Device)
	case "nvenc":
		return "NVENC — NVIDIA"
	case "software":
		return "software libx265 (no GPU)"
	}
	return "unknown"
}

// DetectEncoder scans the system for available hardware encoders and returns
// the best one found, along with human-readable detection notes.
func DetectEncoder() (EncoderConfig, []string) {
	var notes []string

	// VAAPI: AMD and Intel expose DRM render nodes.
	devices, _ := filepath.Glob("/dev/dri/renderD*")
	if len(devices) > 0 {
		enc := EncoderConfig{Type: "vaapi", Device: devices[0]}
		notes = append(notes, fmt.Sprintf("found VAAPI device: %s", devices[0]))
		if len(devices) > 1 {
			notes = append(notes, fmt.Sprintf("  (%d devices total; using first — override with --vaapi-device)", len(devices)))
		}
		return enc, notes
	}
	notes = append(notes, "no VAAPI devices found under /dev/dri/")

	// NVENC: NVIDIA exposes /dev/nvidia* and nvidia-smi.
	if err := exec.Command("nvidia-smi").Run(); err == nil {
		notes = append(notes, "found NVIDIA GPU via nvidia-smi")
		return EncoderConfig{Type: "nvenc"}, notes
	}
	notes = append(notes, "nvidia-smi not found or failed")

	// Software fallback — always works but slow.
	notes = append(notes, "falling back to software encoding (libx265)")
	return EncoderConfig{Type: "software"}, notes
}

func LoadEncoderConfig() EncoderConfig {
	dir, err := configDir()
	if err != nil {
		return EncoderConfig{}
	}
	data, err := os.ReadFile(filepath.Join(dir, "encoder.json"))
	if err != nil {
		return EncoderConfig{}
	}
	var enc EncoderConfig
	json.Unmarshal(data, &enc)
	return enc
}

func SaveEncoderConfig(enc EncoderConfig) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(enc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "encoder.json"), data, 0600)
}

type FileRecord struct {
	Path         string `json:"path"`
	OutputPath   string `json:"output_path,omitempty"`
	OriginalSize int64  `json:"original_size"`
	SavedBytes   int64  `json:"saved_bytes"`
	Codec        string `json:"codec"`
	Error        string `json:"error,omitempty"`
}

type RunRecord struct {
	RunID      string       `json:"run_id,omitempty"`
	Timestamp  time.Time    `json:"timestamp"`
	Files      []FileRecord `json:"files"`
	TotalSaved int64        `json:"total_saved"`
	Converted  int          `json:"converted"`
	Failed     int          `json:"failed"`
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "media-convert")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func LoadToken() string {
	dir, err := configDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return ""
	}
	return string(data)
}

func SaveToken(token string) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0600)
}

func AppendRun(record RunRecord) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "history.json"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(record)
}

func PrintHistory() error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "history.json"))
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No history yet.")
			return nil
		}
		return err
	}

	var records []RunRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var r RunRecord
		if err := dec.Decode(&r); err != nil {
			break
		}
		records = append(records, r)
	}

	if len(records) == 0 {
		fmt.Println("No history yet.")
		return nil
	}

	// Group consecutive records that share the same non-empty RunID into one
	// display entry. Old records (RunID=="") each form their own group.
	type group struct {
		runID      string
		timestamp  time.Time
		files      []FileRecord
		converted  int
		failed     int
		totalSaved int64
	}
	var groups []group
	for _, r := range records {
		if r.RunID != "" && len(groups) > 0 && groups[len(groups)-1].runID == r.RunID {
			g := &groups[len(groups)-1]
			g.files = append(g.files, r.Files...)
			g.converted += r.Converted
			g.failed += r.Failed
			g.totalSaved += r.TotalSaved
		} else {
			groups = append(groups, group{
				runID:      r.RunID,
				timestamp:  r.Timestamp,
				files:      append([]FileRecord(nil), r.Files...),
				converted:  r.Converted,
				failed:     r.Failed,
				totalSaved: r.TotalSaved,
			})
		}
	}

	var totalSaved int64
	var totalConverted, totalFailed int
	for i, g := range groups {
		fmt.Printf("Run %d — %s: %d converted, %d failed, %s saved\n",
			i+1,
			g.timestamp.Local().Format("2006-01-02 15:04"),
			g.converted,
			g.failed,
			formatSize(g.totalSaved),
		)
		for _, f := range g.files {
			if f.Error != "" {
				fmt.Printf("  ✗  %s\n     error: %s\n", f.Path, f.Error)
			} else {
				out := f.OutputPath
				if out == "" {
					out = f.Path
				}
				fmt.Printf("  ✓  %s → %s (%s saved)\n", f.Path, out, formatSize(f.SavedBytes))
			}
		}
		fmt.Println()
		totalSaved += g.totalSaved
		totalConverted += g.converted
		totalFailed += g.failed
	}

	fmt.Printf("Total: %d converted, %d failed, %s saved across %d run(s)\n",
		totalConverted, totalFailed, formatSize(totalSaved), len(groups))
	return nil
}
