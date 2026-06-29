package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func FinalPath(src string, replace bool) string {
	dir := filepath.Dir(src)
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	if replace {
		return filepath.Join(dir, stem+".mkv")
	}
	return filepath.Join(dir, stem+".h265.mkv")
}

func TempPath(src, tempDir string) string {
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	dir := filepath.Dir(src)
	if tempDir != "" {
		dir = tempDir
	}
	return filepath.Join(dir, stem+".tmp.mkv")
}

// EncodeAndVerify runs ffmpeg and verifies the output, returning the temp file path.
// The caller is responsible for committing or cleaning up the temp file.
func EncodeAndVerify(src string, enc EncoderConfig, qp int, tempDir string) (tmpPath string, err error) {
	tmp := TempPath(src, tempDir)
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()

	args := encoderArgs(enc, src, tmp, qp)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return "", fmt.Errorf("ffmpeg failed: %w", runErr)
	}

	if err = verifyOutput(src, tmp); err != nil {
		return "", fmt.Errorf("verification failed: %w", err)
	}

	return tmp, nil
}

// CommitTemp moves the verified temp file to its final destination and
// optionally removes the original. Returns bytes saved.
func CommitTemp(src, tmpPath string, replace bool) (savedBytes int64, err error) {
	srcSize, err := fileSize(src)
	if err != nil {
		os.Remove(tmpPath)
		return 0, err
	}

	final := FinalPath(src, replace)
	if err = moveFile(tmpPath, final); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("move failed: %w", err)
	}

	if replace && src != final {
		if err = os.Remove(src); err != nil {
			return 0, fmt.Errorf("removing original: %w", err)
		}
	}

	dstSize, _ := fileSize(final)
	return srcSize - dstSize, nil
}

func verifyOutput(src, dst string) error {
	dstCodec, err := getVideoCodec(dst)
	if err != nil {
		return fmt.Errorf("could not probe output: %w", err)
	}
	if !isHEVC(dstCodec) {
		return fmt.Errorf("output codec is %q, expected hevc", dstCodec)
	}

	srcDur, err := getDuration(src)
	if err != nil {
		return fmt.Errorf("could not get source duration: %w", err)
	}
	dstDur, err := getDuration(dst)
	if err != nil {
		return fmt.Errorf("could not get output duration: %w", err)
	}

	if srcDur > 0 && math.Abs(dstDur-srcDur)/srcDur > 0.01 {
		return fmt.Errorf("duration mismatch: src=%.1fs dst=%.1fs", srcDur, dstDur)
	}

	return nil
}

func getDuration(path string) (float64, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "N/A" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

func encoderArgs(enc EncoderConfig, src, dst string, qp int) []string {
	tail := []string{"-c:a", "copy", "-c:s", "copy", "-map", "0", "-y", dst}
	switch enc.Type {
	case "nvenc":
		return append([]string{
			"-i", src,
			"-c:v", "hevc_nvenc",
			"-rc", "constqp",
			"-qp", strconv.Itoa(qp),
		}, tail...)
	case "software":
		return append([]string{
			"-i", src,
			"-c:v", "libx265",
			"-crf", strconv.Itoa(qp),
		}, tail...)
	default: // vaapi
		// Hardware decode → GPU format conversion → hardware encode (zero-copy).
		// scale_vaapi handles 8-bit and 10-bit HDR input on the GPU, avoiding
		// the expensive CPU-side 10-bit→8-bit conversion that bottlenecks 4K HDR.
		return append([]string{
			"-hwaccel", "vaapi",
			"-hwaccel_device", enc.Device,
			"-hwaccel_output_format", "vaapi",
			"-i", src,
			"-vf", "scale_vaapi=format=nv12",
			"-c:v", "hevc_vaapi",
			"-rc_mode", "CQP",
			"-qp", strconv.Itoa(qp),
		}, tail...)
	}
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err = out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

func isCrossDevice(err error) bool {
	if le, ok := err.(*os.LinkError); ok {
		if se, ok := le.Err.(syscall.Errno); ok {
			return se == syscall.EXDEV
		}
	}
	return false
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
