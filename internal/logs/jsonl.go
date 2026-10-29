package logs

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Hash returns a short stable hash of parts. History keys use it so raw session
// ids, request ids and file paths are never stored.
func Hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// ReadLine returns the next line without its newline. complete is false when
// the data ended without one (the writer may still be mid-line). Lines longer
// than maxLen are drained and returned as tooLong with no data, so one huge line
// cannot exhaust memory. err is io.EOF at the end of the file.
func ReadLine(r *bufio.Reader, maxLen int) (data []byte, complete, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, e := r.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > maxLen {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case e == nil:
			if tooLong {
				return nil, true, true, nil
			}
			return bytes.TrimSuffix(buf, []byte{'\n'}), true, false, nil
		case errors.Is(e, bufio.ErrBufferFull):
			continue
		default: // io.EOF or a read error
			return buf, false, tooLong, e
		}
	}
}
