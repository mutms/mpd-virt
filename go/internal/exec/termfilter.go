package exec

import (
	"bytes"
	"io"
)

// Terminal-output sanitizer. Remote commands run on a box mpd-virt's own
// threat model calls compromised, and their output lands on the developer's
// terminal — which interprets escape sequences. OSC 52 writes the clipboard,
// title sequences dress up a later phish, and terminal emulators have a
// history of worse. So: everything captured from an external command is
// scrubbed before anyone parses or prints it, and the live-streamed ssh
// output goes through the same filter.
//
// The filter is a whitelist, not a blacklist: printable bytes (UTF-8
// included), newline, tab and carriage return pass; SGR color sequences
// (ESC [ … m — parameters limited to digits and ';') pass so bootstrap
// output keeps its colors; every other escape sequence and control byte is
// dropped whole. CSI/OSC/DCS sequences are consumed to their terminator so
// a stripped sequence leaves no residue.

// filterState tracks a sequence in progress across Write boundaries.
type filterState int

const (
	fsPlain    filterState = iota
	fsEsc                  // seen ESC
	fsEscInter             // ESC + intermediate byte(s) (0x20–0x2F) — until a final byte
	fsCSI                  // seen ESC [ — collecting until final byte
	fsOSC                  // seen ESC ] or ESC P or ESC ^ or ESC _ — until BEL / ESC \
	fsOSCEsc               // inside OSC, seen ESC (possible ST)
)

// sanitizer is the streaming state machine. The zero value is ready to use.
type sanitizer struct {
	state filterState
	csi   []byte // parameter bytes of a CSI sequence in progress
}

// feed pushes one chunk through the filter, appending clean output to dst
// and returning it. Sequences split across chunks are handled by the carried
// state; a sequence still open at stream end is simply never emitted.
func (s *sanitizer) feed(dst, chunk []byte) []byte {
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]
		switch s.state {
		case fsPlain:
			switch {
			case b == 0x1b:
				s.state = fsEsc
			case b == '\n' || b == '\t' || b == '\r':
				dst = append(dst, b)
			case b < 0x20 || b == 0x7f:
				// other C0 controls and DEL: drop
			default:
				dst = append(dst, b)
			}
		case fsEsc:
			switch {
			case b == '[':
				s.state = fsCSI
				s.csi = s.csi[:0]
			case b == ']' || b == 'P' || b == '^' || b == '_': // OSC, DCS, PM, APC — string sequences
				s.state = fsOSC
			case b >= 0x20 && b <= 0x2f:
				// intermediate byte(s) — charset select (ESC ( 0) and
				// friends carry a final byte after them; drop it all
				s.state = fsEscInter
			default:
				// two-byte escape (keypad modes, RIS, …): drop both
				s.state = fsPlain
			}
		case fsEscInter:
			if b < 0x20 || b > 0x2f { // final byte ends the sequence
				s.state = fsPlain
			}
		case fsCSI:
			if b >= 0x40 && b <= 0x7e { // final byte ends the sequence
				if b == 'm' && isSGRParams(s.csi) {
					dst = append(dst, 0x1b, '[')
					dst = append(dst, s.csi...)
					dst = append(dst, 'm')
				}
				s.state = fsPlain
			} else if len(s.csi) < 64 {
				s.csi = append(s.csi, b)
			}
		case fsOSC:
			switch b {
			case 0x07: // BEL terminates
				s.state = fsPlain
			case 0x1b:
				s.state = fsOSCEsc
			}
		case fsOSCEsc:
			if b == '\\' { // ST (ESC \) terminates
				s.state = fsPlain
			} else {
				s.state = fsOSC // stray ESC inside the string; keep consuming
			}
		}
	}
	return dst
}

// isSGRParams reports whether CSI parameter bytes are a plain SGR parameter
// list — digits and semicolons only. Anything fancier is dropped with the
// rest of the non-SGR sequences.
func isSGRParams(p []byte) bool {
	for _, b := range p {
		if (b < '0' || b > '9') && b != ';' {
			return false
		}
	}
	return true
}

// Sanitize scrubs a captured output blob in one call.
func Sanitize(s string) string {
	var sn sanitizer
	return string(sn.feed(make([]byte, 0, len(s)), []byte(s)))
}

// filterWriter streams through a sanitizer into an underlying writer. Used
// for ssh's live output (Run): the remote side must not talk to the
// developer's terminal in escape sequences.
type filterWriter struct {
	w  io.Writer
	sn sanitizer
	// buf is reused between writes to avoid per-chunk allocation.
	buf bytes.Buffer
}

func newFilterWriter(w io.Writer) *filterWriter { return &filterWriter{w: w} }

// Write filters the chunk and forwards the clean bytes. It always reports
// the full input length as written — dropped bytes are the contract, not an
// error.
func (f *filterWriter) Write(p []byte) (int, error) {
	f.buf.Reset()
	clean := f.sn.feed(f.buf.Bytes(), p)
	if len(clean) > 0 {
		if _, err := f.w.Write(clean); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
