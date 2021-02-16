package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"unicode/utf8"

	"github.com/pborman/ansi"
)

// templateWriter writes out rendered data line by line.
type templateWriter struct {
	*template
	io.Writer
	buf bytes.Buffer
}

// Write passes p to the underlying template for use as the {text} placeholder
// during rendering. A trailing newline is expected and is discarded while
// rendering the template, but appended once rendering is done, such that the
// output consists of the rendered bytes plus a newline.
func (w *templateWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	if _, err = w.template.render(&w.buf, p[:len(p)-1]); err != nil {
		return
	}
	w.buf.WriteRune('\n')
	_, err = w.Writer.Write(w.buf.Bytes())
	w.buf.Reset()
	return n, err
}

func newLinewiseWriter(w io.Writer) *linewiseWriter {
	return &linewiseWriter{
		Writer: w,
		buf:    make([]byte, 0, 1024),
	}
}

// linewiseWriter accumulates data until a newline is encountered, dumping it
// all at once.
type linewiseWriter struct {
	io.Writer
	buf []byte
}

func (w *linewiseWriter) Write(p []byte) (n int, err error) {
	for {
		idx := bytes.IndexByte(p, '\n')
		if idx == -1 {
			break
		}
		w.buf = append(w.buf, p[:idx+1]...)
		if _, err = w.Writer.Write(w.buf); err != nil {
			return
		}
		w.buf = w.buf[:0]
		p = p[idx+1:]
		n += idx + 1
	}
	n += len(p)
	w.buf = append(w.buf, p...)
	return
}

func (w *linewiseWriter) Close() (err error) {
	if len(w.buf) > 0 {
		_, err = w.Writer.Write(append(w.buf, '\n'))
		w.buf = w.buf[:0]
	}
	if c, ok := w.Writer.(io.Closer); ok {
		err = c.Close()
	}
	return
}

func newCloseWriter(w io.Writer, close func(io.Writer) error) *closeWriter {
	ic := &closeWriter{Writer: w}
	nop := func(io.Writer) error { return nil }
	ic.close = func(io.Writer) error {
		ic.close = nop
		return close(w)
	}
	return ic
}

// closeWriter calls its close method when Closed.
type closeWriter struct {
	io.Writer
	close func(io.Writer) error
}

func (w *closeWriter) Close() error {
	err := w.close(w.Writer)
	if c, ok := w.Writer.(io.Closer); ok {
		if cerr := c.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

// ansiStripper strips ANSI escape sequences of any kind.
type ansiStripper struct {
	io.Writer
}

func (w *ansiStripper) Write(p []byte) (n int, err error) {
	n = len(p)
	if p, err = ansi.Strip(p); err != nil {
		return 0, err
	}
	if _, err = w.Writer.Write(p); err != nil {
		return 0, err
	}
	return
}

func (w *ansiStripper) Close() error {
	if c, ok := w.Writer.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// quoteEscaper escapes unescaped single and double quotes unconditionally.
type quoteEscaper struct {
	io.Writer
	buf  bytes.Buffer
	prev rune
}

func (w *quoteEscaper) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		r, sz := utf8.DecodeRune(p)
		var pr rune
		pr, w.prev = w.prev, r
		switch r {
		case '\'', '"':
			if pr != '\\' {
				w.buf.WriteRune('\\')
			}
		}
		w.buf.WriteRune(r)
		p = p[sz:]
	}
	_, err := w.Writer.Write(w.buf.Bytes())
	w.buf.Reset()
	return n, err
}

// newInterlockedWriterPair creates a pair of Writers whose Write method is
// protected by the same mutex, such that neither one of them can mangle the
// output of the other.
func newInterlockedWriterPair(a, b io.Writer) (io.Writer, io.Writer) {
	mu := new(sync.Mutex)
	a = &interlockedWriter{Mutex: mu, Writer: a}
	b = &interlockedWriter{Mutex: mu, Writer: b}
	return a, b
}

type interlockedWriter struct {
	*sync.Mutex
	io.Writer
}

func (w *interlockedWriter) Write(p []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	return w.Writer.Write(p)
}

// byteCounter counts how many bytes it writes.
type byteCounter struct {
	io.Writer
	n *uint64
}

func (w *byteCounter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	*w.n += uint64(n)
	return n, err
}

func newFileRotator(path string, maxSize int64, maxCount int) (*fileRotator, error) {
	f, err := openLogfile(path)
	if err != nil {
		return nil, err
	}

	r := &fileRotator{
		file:     f,
		maxSize:  maxSize,
		maxCount: maxCount,
		fileRe:   regexp.MustCompile(fmt.Sprintf(`%s\.\d+`, regexp.QuoteMeta(f.Name()))),
	}

	// Ensure we have an ordered list of files.
	if err := r.reorder(0); err != nil {
		return nil, err
	}

	// If we're limited to a number of logfiles, drop whatever falls outside
	// the range.
	if maxCount > 0 {
		if err := r.slice(maxCount); err != nil {
			return nil, err
		}
	}
	return r, nil
}

type fileRotator struct {
	file      *os.File
	maxSize   int64
	maxCount  int
	fileRe    *regexp.Regexp
	fileCount int // current file count
}

func (w *fileRotator) spaceLeft() (n int64) {
	stat, err := w.file.Stat()
	if err != nil {
		return
	}
	n = w.maxSize - stat.Size()
	if n < 0 {
		n = 0
	}
	return
}

func (w *fileRotator) Write(p []byte) (n int, err error) {
	defer func() {
		if err != nil {
			err = w.err(err)
		}
	}()
	if int64(len(p)) > w.spaceLeft() {
		if err = w.rotate(); err != nil {
			return
		}
	}
	return w.file.Write(p)
}

func (w *fileRotator) Close() error {
	return w.file.Close()
}

func (w *fileRotator) rotate() (err error) {
	defer func() {
		if err == nil {
			err = notice(w.file, "logfile turned over")
		}
	}()
	if w.maxCount == 0 {
		return w.truncate()
	}
	if w.fileCount == w.maxCount {
		if err := w.dropLast(); err != nil {
			return err
		}
	}
	return w.prependCurrent()
}

// prependCurrent prepends <file> to the head of the file list under <file>.0
// and recreates <file>.
func (w *fileRotator) prependCurrent() error {
	// Move <file>.0 to <file>.1, etc.
	if err := w.shiftRight(); err != nil {
		return err
	}

	// Copy <file> to <file>.0 and recreate <file>.
	old, new := w.file.Name(), w.fileNameAt(0)
	if err := w.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(old, new); err != nil {
		return err
	}

	// Touch the original file.
	f, err := openLogfile(old)
	if err != nil {
		return err
	}
	w.file = f

	if w.fileCount < w.maxCount {
		w.fileCount++
	}
	return nil
}

func (w *fileRotator) shiftRight() error {
	if err := w.slice(w.fileCount); err != nil {
		return err
	}
	return w.reorder(1)
}

func (w *fileRotator) dropLast() error {
	return w.slice(w.fileCount - 1)
}

// slice drops files that fall outside the range of [0, to).
func (w *fileRotator) slice(to int) error {
	fs := w.files()
	switch {
	case to < 0:
		to = 0
	case to > len(fs):
		to = len(fs)
	}

	// Drop whatever happens to lie outside of fs[:to].
	for _, f := range fs[to:] {
		if err := os.Remove(f); err != nil {
			return err
		}
		w.fileCount--
	}
	return nil
}

// reorder reorders files such that the first file ends with a suffix that
// corresponds to startAt, and increments subsequent ones.
func (w *fileRotator) reorder(startAt int) error {
	fs := w.files()

	// Start renaming files from the end of the list, such that each file has
	// a slot to its "right-side" to accommodate it.
	for i := len(fs) - 1; i >= 0; i-- {
		old := fs[i]
		new := w.fileNameAt(startAt + i)
		if old != new {
			if err := os.Rename(old, new); err != nil {
				return err
			}
		}
	}
	w.fileCount = len(fs)
	return nil
}

// files returns the list of old logfiles (i.e., everything but w.file), with the
// newest file (<file>.0) at the start of the slice.
func (w *fileRotator) files() (res []string) {
	fs, err := ioutil.ReadDir(filepath.Dir(w.file.Name()))
	if err != nil {
		return
	}
	for _, f := range fs {
		if w.fileRe.MatchString(f.Name()) && !f.IsDir() {
			res = append(res, f.Name())
		}
	}
	return
}

// fileNameAt generates a filename that corresponds to <file>.<idx>.
func (w *fileRotator) fileNameAt(idx int) string {
	return fmt.Sprintf("%s.%s", w.file.Name(), w.nthFileSuffix(idx))
}

func (w *fileRotator) nthFileSuffix(n int) string {
	return fmt.Sprintf("%0*d", countDigits(w.maxCount-1), n)
}

func (w *fileRotator) err(err error) error {
	if err == nil {
		return nil
	}
	return w.errf("%s", err)
}

func (w *fileRotator) errf(s string, args ...interface{}) error {
	return fmt.Errorf("rotate: %s", fmt.Sprintf(s, args...))
}

const (
	logMode  = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	logPerms = 0644
)

func openLogfile(path string) (*os.File, error) {
	return os.OpenFile(path, logMode, logPerms)
}
