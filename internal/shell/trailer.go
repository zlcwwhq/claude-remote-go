package shell

import (
	"bytes"
	"io"
)

// maxTrailer bounds how much output is held back while waiting for the end
// of a possible trailer line.
const maxTrailer = 16 << 10

// trailerFilter streams a remote command's stdout through to w while picking
// out the trailer line the remote script prints last: marker + the remote
// working directory + "\n". The trailer is removed from the output.
//
// Output is passed through as it arrives; only bytes that might be the start
// of the marker are held back until it is clear whether they are.
type trailerFilter struct {
	w      io.Writer
	marker []byte
	buf    []byte
	err    error

	// pwd is the directory from the last trailer seen; found reports whether
	// there was one.
	pwd   string
	found bool
}

func newTrailerFilter(w io.Writer, marker string) *trailerFilter {
	return &trailerFilter{w: w, marker: []byte(marker)}
}

func (f *trailerFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	f.scan()
	return len(p), nil
}

func (f *trailerFilter) scan() {
	for {
		i := bytes.Index(f.buf, f.marker)
		if i < 0 {
			f.emit(len(f.buf) - partialSuffix(f.buf, f.marker))
			return
		}
		f.emit(i)
		rest := f.buf[len(f.marker):]
		nl := bytes.IndexByte(rest, '\n')
		switch {
		case nl < 0 && len(rest) <= maxTrailer:
			return // wait for the rest of the line
		case nl > 0 && rest[0] == '/':
			f.pwd, f.found = string(rest[:nl]), true
			f.buf = rest[nl+1:]
		default:
			// The marker text appeared in ordinary output (e.g. `ps` listing
			// this very script); pass it through.
			f.emit(len(f.marker))
		}
	}
}

// emit writes the first n held bytes through.
func (f *trailerFilter) emit(n int) {
	if n <= 0 {
		return
	}
	if f.err == nil {
		_, f.err = f.w.Write(f.buf[:n])
	}
	f.buf = f.buf[n:]
}

// Close writes out anything still held back.
func (f *trailerFilter) Close() error {
	f.emit(len(f.buf))
	return f.err
}

// partialSuffix returns the length of the longest suffix of b that is a
// proper prefix of marker.
func partialSuffix(b, marker []byte) int {
	n := min(len(b), len(marker)-1)
	for ; n > 0; n-- {
		if bytes.HasSuffix(b, marker[:n]) {
			return n
		}
	}
	return 0
}
