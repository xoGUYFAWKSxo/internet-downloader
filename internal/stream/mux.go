package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNeedFFmpeg is returned when separate video and audio must be joined and
// FFmpeg isn't installed.
var ErrNeedFFmpeg = errors.New("this stream keeps its video and audio separate, and joining them needs FFmpeg, which isn't installed (put ffmpeg.exe next to engine.exe)")

// FFmpeg finds ffmpeg.exe next to the running program, where the installer
// puts it, then on PATH. It returns "" when there is none.
func FFmpeg() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "ffmpeg.exe")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// Ext is the extension of the file the tracks become. With FFmpeg the result
// is an ordinary MP4 (WebM or MKV for WebM tracks); without it a single track
// is kept in its own container.
func Ext(tracks []Track, ffmpeg bool) (string, error) {
	if len(tracks) > 1 && !ffmpeg {
		return "", ErrNeedFFmpeg
	}
	webm := 0
	for _, t := range tracks {
		if t.Format == "webm" {
			webm++
		}
	}
	audio := len(tracks) == 1 && tracks[0].Kind == Audio
	f := tracks[0].Format
	switch {
	case webm == len(tracks):
		return ".webm", nil
	case webm > 0:
		return ".mkv", nil
	case f == "mp3":
		return ".mp3", nil
	case audio && (ffmpeg || f == "mp4"):
		return ".m4a", nil
	case !ffmpeg && f == "ts":
		return ".ts", nil
	case !ffmpeg && f == "aac":
		return ".aac", nil
	}
	return ".mp4", nil
}

// Mux joins the input tracks into out without re-encoding. out's extension
// picks the container.
func Mux(ctx context.Context, ffmpeg string, inputs []string, out string) error {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
	for _, in := range inputs {
		args = append(args, "-i", in)
	}
	if len(inputs) == 1 {
		// Leave out data streams (timed ID3 in HLS) that MP4 can't hold.
		args = append(args, "-map", "0:v?", "-map", "0:a?")
	} else {
		args = append(args, "-map", "0:v:0", "-map", "1:a:0")
	}
	args = append(args, "-c", "copy")
	switch strings.ToLower(filepath.Ext(out)) {
	case ".mp4", ".m4a":
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	hide(cmd)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = "..." + msg[len(msg)-300:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("FFmpeg couldn't join the tracks: %s", msg)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		return errors.New("FFmpeg finished but wrote no file")
	}
	return nil
}
