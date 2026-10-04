package proxy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu      sync.Mutex
	out     io.Writer
	Verbose bool
}

var Log = &Logger{out: os.Stderr}

func (l *Logger) SetOutput(w io.Writer) {
	l.mu.Lock()
	l.out = w
	l.mu.Unlock()
}

var domainRe = regexp.MustCompile(`(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}`)

// censor hides the CF proxy domains in logs, like the Python version.
func censor(s string) string {
	return domainRe.ReplaceAllStringFunc(s, func(d string) string {
		n := strings.ToLower(strings.TrimSuffix(d, "."))
		if n == "telegram.org" || strings.HasSuffix(n, ".telegram.org") ||
			strings.HasSuffix(n, ".log") || strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".txt") {
			return d
		}
		parts := strings.Split(d, ".")
		for i := 0; i < len(parts)-1; i++ {
			p := parts[i]
			h := len(p) / 2
			parts[i] = p[:h] + strings.Repeat("*", len(p)-h)
		}
		return strings.Join(parts, ".")
	})
}

func (l *Logger) log(level, format string, args ...any) {
	msg := censor(fmt.Sprintf(format, args...))
	line := time.Now().Format("15:04:05") + "  " + level + "  " + msg + "\n"
	l.mu.Lock()
	io.WriteString(l.out, line)
	l.mu.Unlock()
}

func (l *Logger) Debugf(f string, a ...any) {
	if l.Verbose {
		l.log("DEBUG", f, a...)
	}
}
func (l *Logger) Infof(f string, a ...any)  { l.log("INFO ", f, a...) }
func (l *Logger) Warnf(f string, a ...any)  { l.log("WARN ", f, a...) }
func (l *Logger) Errorf(f string, a ...any) { l.log("ERROR", f, a...) }

// RotatingFile is a size-capped log file with one backup (.1).
type RotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func OpenRotatingFile(path string, maxBytes int64) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	st, _ := f.Stat()
	r := &RotatingFile{path: path, max: maxBytes, f: f}
	if st != nil {
		r.size = st.Size()
	}
	return r, nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.max > 0 && r.size+int64(len(p)) > r.max {
		r.f.Close()
		_ = os.Remove(r.path + ".1")
		_ = os.Rename(r.path, r.path+".1")
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		r.f = f
		r.size = 0
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}
