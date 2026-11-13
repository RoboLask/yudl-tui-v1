package main

// yudl-tui-v1 — YouTube-загрузчик с терминальным интерфейсом (bubbletea).
//
// Запуск:  go run main.go
// Сборка:  go build -o bin/yudl.exe .

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/ulikunitz/xz"
)

const (
	AppName        = "yudl-tui-v1"
	CurrentVersion = "1"
)

// ==== настройки ====

const (
	ColorRed       = "red"
	ColorGreen     = "green"
	ColorTurquoise = "turquoise"
)

type Settings struct {
	OutputPath string `json:"output_path"`
	Language   string `json:"language"`
	ColorTheme string `json:"color_theme"`
}

func normalizeColor(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ColorGreen:
		return ColorGreen
	case ColorTurquoise:
		return ColorTurquoise
	default:
		return ColorRed
	}
}

// colorHue — базовый оттенок градиента для выбранного цвета.
func colorHue(name string) float64 {
	switch normalizeColor(name) {
	case ColorGreen:
		return 120
	case ColorTurquoise:
		return 175
	default:
		return 0
	}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// appDir — папка программы: настройки и FFmpeg лежат рядом с ней.
func appDir() string {
	if wd, err := os.Getwd(); err == nil && dirWritable(wd) {
		return wd
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		return filepath.Dir(exe)
	}
	return "."
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".yudl-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func settingsPath() string { return filepath.Join(appDir(), "settings.json") }

// defaultDownloadDir — папка загрузок по умолчанию.
func defaultDownloadDir() string {
	h := homeDir()
	candidates := []string{filepath.Join(h, "Downloads")}
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" {
		candidates = append(candidates, filepath.Join(h, "Загрузки"))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return candidates[0]
}

// stripJSONComments убирает //-комментарии, чтобы settings.json можно было
// комментировать руками. Строки в кавычках не трогает.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(data) && data[i+1] == '/' {
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, data[i])
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

func loadSettings() Settings {
	s := Settings{OutputPath: defaultDownloadDir(), Language: "en", ColorTheme: ColorTurquoise}
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return s
	}
	var raw map[string]any
	if json.Unmarshal(stripJSONComments(data), &raw) != nil {
		return s
	}
	if v, ok := raw["output_path"].(string); ok && v != "" {
		s.OutputPath = filepath.FromSlash(v)
	}
	if v, ok := raw["language"].(string); ok && (v == "en" || v == "uk") {
		s.Language = v
	}
	if v, ok := raw["color_theme"].(string); ok && v != "" {
		s.ColorTheme = normalizeColor(v)
	}
	return s
}

func jsonString(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// save пишет settings.json с комментариями, которые можно читать и править.
func (s Settings) save() {
	out := "{\n" +
		"  // Папка, куда сохраняются загрузки. Можно указать любую.\n" +
		"  \"output_path\": " + jsonString(s.OutputPath) + ",\n\n" +
		"  // Язык интерфейса: \"en\" (English) или \"uk\" (Українська).\n" +
		"  \"language\": " + jsonString(s.Language) + ",\n\n" +
		"  // Цвет градиента: \"red\", \"green\" или \"turquoise\".\n" +
		"  \"color_theme\": " + jsonString(s.ColorTheme) + "\n" +
		"}\n"
	_ = os.WriteFile(settingsPath(), []byte(out), 0o644)
}

// ==== переводы (en / uk) ====

var translations = map[string]map[string]string{
	"en": {
		"ready":             "Ready",
		"initializing":      "Initializing...",
		"merging":           "Merging streams (no re-encode)...",
		"complete":          "Complete",
		"stopped":           "Stopped",
		"error":             "Error!",
		"error_empty_url":   "Enter YouTube URL!",
		"error_invalid_url": "Invalid YouTube URL!",
		"paste_title":       "PASTE A LINK",
		"what_download":     "WHAT TO DOWNLOAD?",
		"video_quality":     "VIDEO QUALITY",
		"audio_format":      "AUDIO FORMAT",
		"ready_download":    "READY TO DOWNLOAD",
		"downloading":       "DOWNLOADING",
		"label_link":        "link",
		"label_format":      "format",
		"label_folder":      "folder",
		"no_reencode":       "no re-encode",
		"stopped_mark":      "■ stopped",
		"type_video":        "Video",
		"type_audio":        "Audio",
		"quality_best":      "Best",
		"hint_best":         "maximum quality",
		"hint_720":          "HD, smaller size",
		"hint_480":          "small file",
		"hint_mp3":          "plays everywhere",
		"hint_m4a":          "source AAC copy, no re-encode",
		"hint_wav":          "uncompressed",
		"hint_flac":         "lossless",
	},
	"uk": {
		"ready":             "Готовий",
		"initializing":      "Ініціалізація...",
		"merging":           "Обʼєднання потоків (без перекодування)...",
		"complete":          "Завершено",
		"stopped":           "Зупинено",
		"error":             "Помилка!",
		"error_empty_url":   "Введіть URL YouTube!",
		"error_invalid_url": "Невірний URL YouTube!",
		"paste_title":       "ВСТАВТЕ ПОСИЛАННЯ",
		"what_download":     "ЩО ЗАВАНТАЖИТИ?",
		"video_quality":     "ЯКІСТЬ ВІДЕО",
		"audio_format":      "ФОРМАТ АУДІО",
		"ready_download":    "ГОТОВО ДО ЗАВАНТАЖЕННЯ",
		"downloading":       "ЗАВАНТАЖЕННЯ",
		"label_link":        "посилання",
		"label_format":      "формат",
		"label_folder":      "папка",
		"no_reencode":       "без перекодування",
		"stopped_mark":      "■ зупинено",
		"type_video":        "Відео",
		"type_audio":        "Аудіо",
		"quality_best":      "Найкраще",
		"hint_best":         "максимальна якість",
		"hint_720":          "HD, менший розмір",
		"hint_480":          "малий файл",
		"hint_mp3":          "грає всюди",
		"hint_m4a":          "копія AAC, без перекодування",
		"hint_wav":          "без стиснення",
		"hint_flac":         "без втрат",
	},
}

func tr(lang, key string) string {
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if v, ok := translations["en"][key]; ok {
		return v
	}
	return key
}

// ==== сеть ====

func httpDownload(url, target string, progress func(string)) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", AppName)
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := target + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	buf := make([]byte, 256*1024)
	var done int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			done += int64(n)
			if progress != nil && total > 0 {
				progress(fmt.Sprintf("%d%%", done*100/total))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	_ = os.Remove(target)
	return os.Rename(tmp, target)
}

// ==== FFmpeg ====

var (
	ffmpegMu  sync.Mutex
	ffmpegDir string
)

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func hasFFmpeg(dir string) bool {
	if dir == "" {
		return false
	}
	_, e1 := os.Stat(filepath.Join(dir, exeName("ffmpeg")))
	_, e2 := os.Stat(filepath.Join(dir, exeName("ffprobe")))
	return e1 == nil && e2 == nil
}

// ffmpegDirPath — папка ffmpeg внутри папки программы.
func ffmpegDirPath() string { return filepath.Join(appDir(), "ffmpeg") }

// appDirs — папки рядом с бинарником программы.
func appDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		dirs = append(dirs, filepath.Dir(file))
	}
	return dirs
}

// FindFFmpeg возвращает папку с ffmpeg/ffprobe или "".
func FindFFmpeg() string {
	ffmpegMu.Lock()
	defer ffmpegMu.Unlock()
	return findFFmpegLocked()
}

func findFFmpegLocked() string {
	if ffmpegDir != "" && hasFFmpeg(ffmpegDir) {
		return ffmpegDir
	}
	if env := strings.TrimSpace(os.Getenv("YUDL_FFMPEG")); env != "" {
		if fi, err := os.Stat(env); err == nil {
			dir := env
			if !fi.IsDir() {
				dir = filepath.Dir(env)
			}
			if hasFFmpeg(dir) {
				ffmpegDir = dir
				return dir
			}
		}
	}

	var candidates []string
	for _, base := range appDirs() {
		candidates = append(candidates,
			filepath.Join(base, "ffmpeg", "bin"),
			filepath.Join(base, "ffmpeg"),
			filepath.Join(base, "bin"),
			base,
		)
	}
	candidates = append(candidates, ffmpegDirPath())
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			`C:\ffmpeg\bin`,
			`C:\Program Files\ffmpeg\bin`,
			`C:\Program Files (x86)\ffmpeg\bin`,
		)
	} else {
		candidates = append(candidates, "/usr/local/bin", "/usr/bin", "/opt/homebrew/bin", "/opt/local/bin")
	}

	for _, dir := range candidates {
		if hasFFmpeg(dir) {
			ffmpegDir = dir
			return dir
		}
	}

	if p, err := exec.LookPath(exeName("ffmpeg")); err == nil {
		dir := filepath.Dir(p)
		ffmpegDir = dir
		return dir
	}
	return ""
}

// EnsureFFmpeg возвращает папку с FFmpeg, скачивая его при необходимости.
func EnsureFFmpeg(progress func(string)) (string, error) {
	if dir := FindFFmpeg(); dir != "" {
		return dir, nil
	}
	notify(progress, "FFmpeg: download...")
	dir, err := downloadFFmpeg(progress)
	if err != nil {
		return "", err
	}
	ffmpegMu.Lock()
	ffmpegDir = dir
	ffmpegMu.Unlock()
	return dir, nil
}

func notify(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}

type ffmpegSource struct {
	url  string
	kind string // "zip" | "tar.xz"
}

// ffmpegSources — откуда качается FFmpeg. Источники пробуются по порядку,
// пока один из них не сработает.
//
//	Windows: yt-dlp/FFmpeg-Builds (GitHub), затем BtbN/FFmpeg-Builds
//	Linux:   BtbN/FFmpeg-Builds (GitHub), затем johnvansickle.com
//	macOS:   osxexperts.net
func ffmpegSources() []ffmpegSource {
	switch runtime.GOOS {
	case "windows":
		if runtime.GOARCH == "arm64" {
			return []ffmpegSource{
				{"https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-winarm64-gpl.zip", "zip"},
				{"https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-winarm64-gpl.zip", "zip"},
			}
		}
		return []ffmpegSource{
			{"https://github.com/yt-dlp/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip", "zip"},
			{"https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip", "zip"},
		}
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return []ffmpegSource{{"https://www.osxexperts.net/ffmpeg71arm.zip", "zip"}}
		}
		return []ffmpegSource{{"https://www.osxexperts.net/ffmpeg71intel.zip", "zip"}}
	default:
		if runtime.GOARCH == "arm64" {
			return []ffmpegSource{
				{"https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-linuxarm64-gpl.tar.xz", "tar.xz"},
				{"https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-arm64-static.tar.xz", "tar.xz"},
			}
		}
		return []ffmpegSource{
			{"https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-linux64-gpl.tar.xz", "tar.xz"},
			{"https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz", "tar.xz"},
		}
	}
}

// fetchBytes скачивает URL в память, сообщая прогресс в процентах.
func fetchBytes(url string, progress func(string)) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", AppName)
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	total := resp.ContentLength
	chunk := make([]byte, 256*1024)
	var done int64
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			done += int64(n)
			if total > 0 {
				notify(progress, fmt.Sprintf("FFmpeg: %d%%", done*100/total))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	return buf.Bytes(), nil
}

func downloadFFmpeg(progress func(string)) (string, error) {
	dir := ffmpegDirPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	var lastErr error
	for _, src := range ffmpegSources() {
		notify(progress, "FFmpeg: download...")
		data, err := fetchBytes(src.url, progress)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src.url, err)
			continue
		}
		if src.kind == "zip" {
			err = extractBinariesZip(data, dir)
		} else {
			err = extractBinariesTarXZ(data, dir)
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src.url, err)
			continue
		}
		if !hasFFmpeg(dir) {
			lastErr = fmt.Errorf("%s: binaries not found in archive", src.url)
			continue
		}
		for _, n := range []string{"ffmpeg", "ffprobe"} {
			_ = os.Chmod(filepath.Join(dir, exeName(n)), 0o755)
		}
		return dir, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no download sources")
	}
	return "", fmt.Errorf("ffmpeg download failed: %w", lastErr)
}

func extractBinariesZip(data []byte, dst string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	wanted := map[string]bool{exeName("ffmpeg"): true, exeName("ffprobe"): true}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if !wanted[name] {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFileFrom(dst, name, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractBinariesTarXZ(data []byte, dst string) error {
	xzr, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tarReader := tar.NewReader(xzr)
	wanted := map[string]bool{exeName("ffmpeg"): true, exeName("ffprobe"): true}
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Base(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || !wanted[name] {
			continue
		}
		if err := writeFileFrom(dst, name, tarReader); err != nil {
			return err
		}
	}
}

func writeFileFrom(dst, name string, r io.Reader) error {
	target := filepath.Join(dst, name)
	tmp := target + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// ==== загрузка через yt-dlp ====
//
// Видео: готовые MP4-потоки склеиваются потоково (без перекодирования).
// Аудио M4A: копируется исходный AAC (--remux-video m4a).
// MP3/WAV/FLAC: один проход извлечения звука — иначе эти форматы не получить.

type DownloadRequest struct {
	URL         string
	OutputPath  string
	FormatType  string // "video" | "audio"
	Quality     string // "best" | "medium" | "low"
	AudioFormat string // "mp3" | "wav" | "flac" | "m4a"
	FFmpegDir   string
}

// streamEvent — событие прогресса.
type streamEvent struct {
	Percent    int
	Downloaded int64
	Total      int64
	Text       string // статус: "merge", "FFmpeg: 42%", ...
}

// ytDlpTarget — куда скачивается yt-dlp при первом запуске.
func ytDlpTarget() string { return filepath.Join(appDir(), exeName("yt-dlp")) }

// ytDlpPath ищет yt-dlp рядом с программой, в кэше и в PATH.
func ytDlpPath() string {
	for _, base := range appDirs() {
		for _, c := range []string{
			filepath.Join(base, exeName("yt-dlp")),
			filepath.Join(base, "bin", exeName("yt-dlp")),
		} {
			if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
				return c
			}
		}
	}
	if fi, err := os.Stat(ytDlpTarget()); err == nil && fi.Mode().IsRegular() && fi.Size() > 1024 {
		return ytDlpTarget()
	}
	if p, err := exec.LookPath(exeName("yt-dlp")); err == nil {
		return p
	}
	return ""
}

// ensureYtDlp скачивает yt-dlp один раз.
func ensureYtDlp(progress func(string)) (string, error) {
	if p := ytDlpPath(); p != "" {
		return p, nil
	}
	target := ytDlpTarget()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	notify(progress, "yt-dlp: download...")
	url := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp"
	if runtime.GOOS == "windows" {
		url += ".exe"
	}
	if err := httpDownload(url, target, progress); err != nil {
		return "", err
	}
	_ = os.Chmod(target, 0o755)
	return target, nil
}

// BuildArgs собирает аргументы yt-dlp.
func BuildArgs(req DownloadRequest) []string {
	args := []string{
		"--newline",
		"--no-color",
		"--progress",
		"--progress-delta", "0.15",
		"--socket-timeout", "30",
		"--retries", "15",
		"--fragment-retries", "15",
		"--extractor-retries", "5",
		"--concurrent-fragments", "1",
		"-o", filepath.Join(req.OutputPath, "%(title)s.%(ext)s"),
	}
	if req.FFmpegDir != "" {
		args = append(args, "--ffmpeg-location", req.FFmpegDir)
	}

	if req.FormatType == "audio" {
		if req.AudioFormat == "m4a" {
			args = append(args,
				"-f", "bestaudio[ext=m4a]/bestaudio[acodec^=mp4a]/bestaudio",
				"--remux-video", "m4a",
			)
		} else {
			args = append(args, "-x", "--audio-format", req.AudioFormat)
			if req.AudioFormat == "flac" || req.AudioFormat == "wav" {
				args = append(args, "--audio-quality", "0")
			}
		}
		return append(args, req.URL)
	}

	height := ""
	switch req.Quality {
	case "medium":
		height = "[height<=720]"
	case "low":
		height = "[height<=480]"
	}
	sel := fmt.Sprintf(
		"bestvideo%s[ext=mp4]+bestaudio[ext=m4a]/best%s[ext=mp4]/bestvideo%s+bestaudio/best%s",
		height, height, height, height,
	)
	args = append(args, "-f", sel, "--merge-output-format", "mp4")
	return append(args, req.URL)
}

// RunDownload запускает yt-dlp и отдаёт прогресс через emit.
func RunDownload(ctx context.Context, req DownloadRequest, emit func(streamEvent)) error {
	bin, err := ensureYtDlp(func(t string) { emit(streamEvent{Text: t, Percent: -1}) })
	if err != nil {
		return fmt.Errorf("yt-dlp not found: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, BuildArgs(req)...)
	if fi, err := os.Stat(req.OutputPath); err == nil && fi.IsDir() {
		cmd.Dir = req.OutputPath
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	go func() { scanProgress(stderr, emit); done <- struct{}{} }()
	go func() { scanProgress(stdout, emit); done <- struct{}{} }()
	<-done
	<-done
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// scanProgress парсит вывод yt-dlp.
func scanProgress(r io.Reader, emit func(streamEvent)) {
	if r == nil {
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	last := -2
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "[download]") && strings.Contains(line, "%"):
			pct, dl, total, ok := parseProgressLine(line)
			if !ok || pct == last {
				continue
			}
			last = pct
			emit(streamEvent{Percent: pct, Downloaded: dl, Total: total})
		case strings.Contains(line, "Merging formats"), strings.HasPrefix(line, "[Merger]"), strings.HasPrefix(line, "[ExtractAudio]"):
			emit(streamEvent{Text: "merge", Percent: 99})
		}
	}
}

func parseProgressLine(line string) (int, int64, int64, bool) {
	fields := strings.Fields(line)
	pct, ok := -1, false
	for _, f := range fields {
		if strings.HasSuffix(f, "%") {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64); err == nil {
				pct, ok = int(v), true
				break
			}
		}
	}
	if !ok {
		return 0, 0, 0, false
	}
	var total int64
	if i := indexOf(fields, "of"); i >= 0 && i+1 < len(fields) {
		total = parseSize(fields[i+1])
	}
	var dl int64
	if total > 0 {
		dl = total * int64(pct) / 100
	}
	return pct, dl, total, true
}

func indexOf(fields []string, s string) int {
	for i, f := range fields {
		if f == s {
			return i
		}
	}
	return -1
}

func parseSize(s string) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	for _, suf := range []struct {
		s string
		m float64
	}{
		{"kib", 1024}, {"mib", 1024 * 1024}, {"gib", 1024 * 1024 * 1024},
		{"kb", 1000}, {"mb", 1000 * 1000}, {"gb", 1000 * 1000 * 1000}, {"b", 1},
	} {
		if strings.HasSuffix(s, suf.s) {
			s = strings.TrimSuffix(s, suf.s)
			mult = suf.m
			break
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(v * mult)
}

func isYouTubeURL(u string) bool {
	lu := strings.ToLower(u)
	return strings.Contains(lu, "youtube.com") || strings.Contains(lu, "youtu.be")
}

// ==== вставка ====

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z~]`)

// printableText оставляет только печатные символы: без управления,
// ANSI-последовательностей, невидимых символов и символа-заменителя.
func printableText(s string) string {
	s = ansiSeq.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f:
			continue
		case r == 0xFFFD: // символ-заменитель
			continue
		case r >= 0x200B && r <= 0x200F, r == 0xFEFF:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizePaste очищает вставленный текст и убирает переводы строк.
func sanitizePaste(s string) string {
	return strings.TrimSpace(printableText(s))
}

// ==== TUI ====

type step int

const (
	stepAskURL step = iota
	stepType
	stepQuality
	stepConfirm
	stepDownloading
	stepDone
)

type option struct {
	label string
	hint  string
}

type tickMsg struct{}

type tuiModel struct {
	lang    string
	outPath string
	baseHue float64
	phase   float64

	step     step
	url      string
	cursor   int
	typeIdx  int
	qualIdx  int
	percent  int
	status   string
	err      string
	quitting bool
	seq      int
}

type tuiMsg struct {
	gen  int
	ev   streamEvent
	err  error
	done bool
}

var tuiEvents = make(chan tuiMsg, 256)

func NewTUI(url string) tuiModel {
	s := loadSettings()
	m := tuiModel{
		lang:    s.Language,
		outPath: s.OutputPath,
		baseHue: colorHue(s.ColorTheme),
		status:  tr(s.Language, "ready"),
	}
	if strings.TrimSpace(url) != "" {
		m.url = strings.TrimSpace(url)
		m.step = stepType
	}
	return m
}

func (m tuiModel) tr(key string) string { return tr(m.lang, key) }

func (m tuiModel) typeOptions() []option {
	return []option{
		{m.tr("type_video"), ""},
		{m.tr("type_audio"), ""},
	}
}

func (m tuiModel) qualityOptions() []option {
	if m.typeIdx == 1 {
		return []option{
			{"MP3", m.tr("hint_mp3")},
			{"M4A", m.tr("hint_m4a")},
			{"WAV", m.tr("hint_wav")},
			{"FLAC", m.tr("hint_flac")},
		}
	}
	return []option{
		{m.tr("quality_best"), m.tr("hint_best")},
		{"720p", m.tr("hint_720")},
		{"480p", m.tr("hint_480")},
	}
}

func tick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m tuiModel) Init() tea.Cmd { return tick() }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.phase += 0.08
		return m, tick()

	case tuiMsg:
		return m.handleDownloadMsg(msg)

	case tea.KeyMsg:
		// вставка из терминала приходит как Runes с флагом Paste
		if msg.Paste && m.step == stepAskURL {
			m.url += sanitizePaste(string(msg.Runes))
			m.err = ""
			return m, nil
		}

		k := msg.String()

		if k == "ctrl+c" {
			if m.step == stepDownloading {
				m.cancelDownload()
			}
			m.quitting = true
			return m, tea.Quit
		}

		if m.step == stepDownloading {
			if k == "esc" {
				m.cancelDownload()
			}
			return m, nil
		}

		if m.step == stepDone {
			switch k {
			case "enter", "q":
				m.quitting = true
				return m, tea.Quit
			case "esc":
				m.step = stepAskURL
				m.url = ""
				m.percent = 0
				m.err = ""
				m.status = m.tr("ready")
				m.cursor = 0
			}
			return m, nil
		}

		// на экране ссылки любые печатные символы идут в URL,
		// поэтому горячие клавиши здесь не перехватываются.
		if m.step == stepAskURL {
			switch k {
			case "esc":
				m.quitting = true
				return m, tea.Quit
			case "enter":
				return m.advance()
			case "backspace", "ctrl+h":
				if len(m.url) > 0 {
					m.url = m.url[:len(m.url)-1]
				}
			default:
				// ctrl/alt-сочетания и управляющие символы в URL не попадают
				if strings.HasPrefix(k, "ctrl+") || strings.HasPrefix(k, "alt+") {
					return m, nil
				}
				if text := printableText(string(msg.Runes)); text != "" {
					m.url += text
					m.err = ""
				}
			}
			return m, nil
		}

		switch k {
		case "esc":
			switch m.step {
			case stepType:
				m.step = stepAskURL
			case stepQuality:
				m.step = stepType
			case stepConfirm:
				m.step = stepQuality
			}
			m.cursor = 0
			return m, nil

		case "up", "k", "shift+tab", "left", "h":
			m.cursor--
			if m.cursor < 0 {
				m.cursor = m.optCount() - 1
			}
			return m, nil

		case "down", "j", "tab", "right", "l":
			m.cursor++
			if m.cursor >= m.optCount() {
				m.cursor = 0
			}
			return m, nil

		case "1", "2", "3", "4":
			idx := int(k[0] - '1')
			if idx < m.optCount() {
				m.cursor = idx
				return m.advance()
			}
			return m, nil

		case "enter":
			return m.advance()
		}
		return m, nil
	}
	return m, nil
}

func (m tuiModel) optCount() int {
	switch m.step {
	case stepType:
		return len(m.typeOptions())
	case stepQuality:
		return len(m.qualityOptions())
	}
	return 1
}

func (m tuiModel) advance() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepAskURL:
		url := strings.TrimSpace(m.url)
		if url == "" {
			m.err = m.tr("error_empty_url")
			return m, nil
		}
		if !isYouTubeURL(url) {
			m.err = m.tr("error_invalid_url")
			return m, nil
		}
		m.err = ""
		m.cursor = 0
		m.step = stepType
	case stepType:
		m.typeIdx = m.cursor
		m.cursor = 0
		m.step = stepQuality
	case stepQuality:
		m.qualIdx = m.cursor
		m.step = stepConfirm
	case stepConfirm:
		return m.startDownload()
	}
	return m, nil
}

var (
	cancelMu sync.Mutex
	cancelFn context.CancelFunc
)

func (m *tuiModel) cancelDownload() {
	cancelMu.Lock()
	if cancelFn != nil {
		cancelFn()
		cancelFn = nil
	}
	cancelMu.Unlock()
	m.seq++
	m.step = stepDone
	m.status = m.tr("stopped")
}

func (m tuiModel) request() DownloadRequest {
	req := DownloadRequest{URL: strings.TrimSpace(m.url), OutputPath: m.outPath}
	if m.typeIdx == 1 {
		req.FormatType = "audio"
		req.AudioFormat = []string{"mp3", "m4a", "wav", "flac"}[m.qualIdx]
	} else {
		req.FormatType = "video"
		req.Quality = []string{"best", "medium", "low"}[m.qualIdx]
	}
	return req
}

func (m tuiModel) startDownload() (tea.Model, tea.Cmd) {
	if err := os.MkdirAll(m.outPath, 0o755); err != nil {
		m.err = err.Error()
		m.step = stepDone
		return m, nil
	}
	req := m.request()
	m.step = stepDownloading
	m.percent = 0
	m.err = ""
	m.status = m.tr("initializing")
	m.seq++
	gen := m.seq

	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancelMu.Lock()
		cancelFn = cancel
		cancelMu.Unlock()

		ffDir, err := EnsureFFmpeg(func(text string) {
			tuiEvents <- tuiMsg{gen: gen, ev: streamEvent{Text: text, Percent: -1}}
		})
		if err != nil {
			tuiEvents <- tuiMsg{gen: gen, done: true, err: err}
			return
		}
		req.FFmpegDir = ffDir
		err = RunDownload(ctx, req, func(ev streamEvent) {
			tuiEvents <- tuiMsg{gen: gen, ev: ev}
		})
		tuiEvents <- tuiMsg{gen: gen, done: true, err: err}
	}()

	return m, waitForEvent()
}

func waitForEvent() tea.Cmd {
	return func() tea.Msg { return <-tuiEvents }
}

func (m tuiModel) handleDownloadMsg(msg tuiMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.seq {
		return m, nil
	}
	if msg.done {
		m.step = stepDone
		if msg.err != nil {
			if msg.err == context.Canceled {
				m.status = m.tr("stopped")
			} else {
				m.err = msg.err.Error()
				m.status = m.tr("error")
			}
			return m, nil
		}
		m.percent = 100
		m.status = m.tr("complete")
		return m, nil
	}
	if msg.ev.Text == "merge" {
		m.percent = 99
		m.status = m.tr("merging")
		return m, waitForEvent()
	}
	if msg.ev.Text != "" && msg.ev.Percent < 0 {
		m.status = msg.ev.Text
		return m, waitForEvent()
	}
	if msg.ev.Percent >= 0 {
		m.percent = msg.ev.Percent
		if msg.ev.Percent >= 100 {
			m.status = m.tr("merging")
		} else if msg.ev.Total > 0 {
			m.status = fmt.Sprintf("%.1f / %.1f MB",
				float64(msg.ev.Downloaded)/1048576, float64(msg.ev.Total)/1048576)
		}
	}
	return m, waitForEvent()
}

// RunTUI запускает мастер. args: [url] [video|audio] [best|720|480|mp3|m4a|wav|flac]
func RunTUI(args ...string) error {
	url := ""
	if len(args) > 0 {
		url = args[0]
	}
	m := NewTUI(url)

	if len(args) > 2 && strings.TrimSpace(url) != "" {
		if strings.EqualFold(args[1], "audio") {
			m.typeIdx = 1
			for i, f := range []string{"mp3", "m4a", "wav", "flac"} {
				if strings.EqualFold(f, args[2]) {
					m.qualIdx = i
				}
			}
		} else {
			m.typeIdx = 0
			switch strings.ToLower(args[2]) {
			case "720", "medium":
				m.qualIdx = 1
			case "480", "low":
				m.qualIdx = 2
			default:
				m.qualIdx = 0
			}
		}
		m.step = stepConfirm
		model, _ := m.startDownload()
		p := tea.NewProgram(model, tea.WithAltScreen())
		_, err := p.Run()
		return err
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// ==== градиент и отрисовка ====

var (
	styleDim   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#8A8A8A"))
	styleFaint = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#555555"))
	styleWhite = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#EEEEEE"))
	styleRed   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5F5F"))
	styleTrack = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#2A2A2A"))
)

// gradColor — переливающийся цвет: оттенок зависит от позиции и фазы анимации.
func (m tuiModel) gradColor(pos, total int) lipgloss.Color {
	if total < 1 {
		total = 1
	}
	hue := math.Mod(m.baseHue+float64(pos)*80/float64(total)+m.phase*30, 360)
	return lipgloss.Color(colorful.Hsl(hue, 0.9, 0.6).Hex())
}

func (m tuiModel) accent() lipgloss.Color { return m.gradColor(0, 1) }

func (m tuiModel) accentStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(m.accent())
}

// gradText красит текст посимвольно в градиент.
func (m tuiModel) gradText(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(m.gradColor(i, len(runes))).Render(string(r)))
	}
	return b.String()
}

// banner — ASCII-логотип yudl-tui-v1.
func (m tuiModel) banner() string {
	rows := []string{
		"██╗   ██╗██╗   ██╗██████╗ ██╗",
		"╚██╗ ██╔╝██║   ██║██╔══██╗██║",
		" ╚████╔╝ ██║   ██║██║  ██║██║",
		"  ╚██╔╝  ██║   ██║██║  ██║██║",
		"   ██║   ╚██████╔╝██████╔╝███████╗",
		"   ╚═╝    ╚═════╝ ╚═════╝ ╚══════╝",
	}
	var b strings.Builder
	for _, row := range rows {
		b.WriteString("  ")
		b.WriteString(m.gradText(row))
		b.WriteString("\n")
	}
	b.WriteString("  ")
	b.WriteString(styleDim.Render(AppName))
	b.WriteString("\n\n")
	return b.String()
}

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	acc := m.accentStyle()
	var b strings.Builder
	b.WriteString(m.banner())

	switch m.step {
	case stepAskURL:
		b.WriteString(m.gradText("  " + m.tr("paste_title")))
		b.WriteString("\n\n")
		if m.url == "" {
			b.WriteString("  " + styleFaint.Render("────────────────────────────────────────") + "\n")
		} else {
			b.WriteString("  " + acc.Render("▸ ") + styleWhite.Render(m.url) + "\n")
			b.WriteString("  " + styleFaint.Render(strings.Repeat("─", minInt(len(m.url)+2, 56))) + "\n")
		}
		if m.err != "" {
			b.WriteString("\n  " + styleRed.Render("✗ "+m.err) + "\n")
		}

	case stepType, stepQuality:
		title := m.tr("what_download")
		opts := m.typeOptions()
		if m.step == stepQuality {
			opts = m.qualityOptions()
			title = m.tr("video_quality")
			if m.typeIdx == 1 {
				title = m.tr("audio_format")
			}
		}
		b.WriteString(m.gradText("  " + title))
		b.WriteString("\n\n")
		b.WriteString(m.listView(opts))

	case stepConfirm:
		req := m.request()
		kind := "MP4 · " + req.Quality
		if req.FormatType == "audio" {
			kind = strings.ToUpper(req.AudioFormat)
			if req.AudioFormat == "m4a" {
				kind += " · " + m.tr("no_reencode")
			}
		}
		b.WriteString(m.gradText("  " + m.tr("ready_download")))
		b.WriteString("\n\n")
		b.WriteString("  " + styleDim.Render(m.tr("label_link")+"   ") + styleWhite.Render(shortURL(req.URL)) + "\n")
		b.WriteString("  " + styleDim.Render(m.tr("label_format")+"   ") + acc.Render(kind) + "\n")
		b.WriteString("  " + styleDim.Render(m.tr("label_folder")+"    ") + styleWhite.Render(req.OutputPath) + "\n")

	case stepDownloading:
		b.WriteString(m.gradText("  " + m.tr("downloading")))
		b.WriteString("\n\n")
		b.WriteString("  " + m.progressView() + "   " + acc.Render(fmt.Sprintf("%3d%%", m.percent)) + "\n")
		b.WriteString("\n  " + styleWhite.Render(m.status) + "\n")

	case stepDone:
		if m.err != "" {
			b.WriteString(styleRed.Render("  ✗ " + m.tr("error")))
			b.WriteString("\n\n")
			b.WriteString("  " + styleWhite.Render(m.err) + "\n")
		} else if m.status == m.tr("stopped") {
			b.WriteString(styleDim.Render("  "+m.tr("stopped_mark")) + "\n")
		} else {
			b.WriteString(m.gradText("  ✓ " + m.tr("complete")))
			b.WriteString("\n\n")
			b.WriteString("  " + styleDim.Render(m.tr("label_folder")+"  ") + styleWhite.Render(m.outPath) + "\n")
		}
	}

	return lipgloss.NewStyle().Padding(1, 1).Render(b.String())
}

// listView — список опций с курсором.
func (m tuiModel) listView(opts []option) string {
	var b strings.Builder
	acc := m.accentStyle()
	for i, o := range opts {
		number := fmt.Sprintf("%d. ", i+1)
		if i == m.cursor {
			b.WriteString("  " + acc.Render("▸ "+number+o.label))
			if o.hint != "" {
				b.WriteString("   " + styleDim.Render(o.hint))
			}
		} else {
			b.WriteString("    " + styleWhite.Render(number+o.label))
			if o.hint != "" {
				b.WriteString("   " + styleFaint.Render(o.hint))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// progressView — широкий градиентный прогресс-бар.
func (m tuiModel) progressView() string {
	const width = 46
	filled := m.percent * width / 100
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(m.gradColor(i, width)).Render("━"))
		} else {
			b.WriteString(styleTrack.Render("━"))
		}
	}
	return b.String()
}

func shortURL(u string) string {
	if len(u) <= 56 {
		return u
	}
	return u[:28] + "…" + u[len(u)-24:]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ==== точка входа ====

func main() {
	args := os.Args[1:]
	clean := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--version", a == "-v":
			fmt.Println(AppName, CurrentVersion)
			return
		case a == "--check-ffmpeg":
			if dir := FindFFmpeg(); dir != "" {
				fmt.Println("ffmpeg:", dir)
			} else {
				fmt.Println("ffmpeg: not found (will be downloaded on first download)")
			}
			return
		case a == "--help", a == "-h":
			usage()
			return
		case a == "--lang" && i+1 < len(args):
			s := loadSettings()
			l := strings.ToLower(args[i+1])
			if l == "en" || l == "uk" {
				s.Language = l
				s.save()
			}
			i++
		case strings.HasPrefix(a, "--lang="):
			s := loadSettings()
			l := strings.ToLower(strings.TrimPrefix(a, "--lang="))
			if l == "en" || l == "uk" {
				s.Language = l
				s.save()
			}
		case a == "--tui":
			// исторический флаг, ничего не делает
		default:
			clean = append(clean, a)
		}
	}
	if err := RunTUI(clean...); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Printf(`%s %s — YouTube downloader

  yudl              интерактивный мастер
  yudl <url>        сразу к выбору формата
  yudl --lang en    язык: en | uk
  yudl --version    версия

Settings: %s
`, AppName, CurrentVersion, settingsPath())
}
