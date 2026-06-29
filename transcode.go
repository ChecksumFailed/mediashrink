package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

func TempPath(src string) string {
	dir := filepath.Dir(src)
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	return filepath.Join(dir, stem+".tmp.mkv")
}

func Transcode(src string, enc EncoderConfig, qp int, replace bool) (savedBytes int64, err error) {
	srcSize, err := fileSize(src)
	if err != nil {
		return 0, err
	}

	tmp := TempPath(src)
	final := FinalPath(src, replace)

	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()

	args := encoderArgs(enc, src, tmp, qp)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return 0, fmt.Errorf("ffmpeg failed: %w", runErr)
	}

	if err = verifyOutput(src, tmp); err != nil {
		return 0, fmt.Errorf("verification failed: %w", err)
	}

	if err = os.Rename(tmp, final); err != nil {
		return 0, fmt.Errorf("rename failed: %w", err)
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
		return append([]string{
			"-vaapi_device", enc.Device,
			"-i", src,
			"-vf", "format=nv12|vaapi,hwupload",
			"-c:v", "hevc_vaapi",
			"-rc_mode", "CQP",
			"-qp", strconv.Itoa(qp),
		}, tail...)
	}
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
