package main

import (
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true,
	".mov": true, ".wmv": true, ".ts": true, ".m2ts": true,
}

type Candidate struct {
	Path  string
	Size  int64
	Codec string
}

func FindCandidates(root string, minBytes int64, skipHEVC bool) ([]Candidate, error) {
	var candidates []Candidate

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !videoExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() < minBytes {
			return nil
		}

		codec, err := getVideoCodec(path)
		if err != nil || codec == "" {
			fmt.Printf("warning: could not probe %s: %v\n", path, err)
			return nil
		}
		if skipHEVC && isHEVC(codec) {
			return nil
		}

		candidates = append(candidates, Candidate{Path: path, Size: info.Size(), Codec: codec})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", root, err)
	}

	return candidates, nil
}

func getVideoCodec(path string) (string, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func isHEVC(codec string) bool {
	codec = strings.ToLower(codec)
	return codec == "hevc" || codec == "h265"
}
