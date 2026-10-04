package proxy

import (
	"encoding/binary"
	"math/rand/v2"
)

func fastRand(b []byte) {
	for i := 0; i < len(b); i += 4 {
		var t [4]byte
		binary.LittleEndian.PutUint32(t[:], rand.Uint32())
		copy(b[i:], t[:])
	}
}

// packetSplitter cuts the client->Telegram byte stream into whole MTProto
// transport packets so each one travels as its own WS message (what
// Telegram's WS endpoint expects). Unlike the Python version it reads the
// lengths from the plaintext we already have, so there is no extra AES pass.
//
// Per chunk: feed(plain) learns the boundaries, then cut(cipher) returns the
// complete packets of ciphertext (CTR keeps offsets identical).
type packetSplitter struct {
	proto    ProtoTag
	disabled bool
	stopNow  bool // framing became unknown inside the current chunk

	hdr    [4]byte
	hdrN   int
	remain int

	pending []byte // ciphertext of an incomplete packet from earlier chunks
	bounds  []int  // packet end offsets inside the current chunk
}

func newPacketSplitter(proto ProtoTag) *packetSplitter {
	return &packetSplitter{proto: proto}
}

func (s *packetSplitter) headerLen() int {
	if s.proto == ProtoAbridged {
		if s.hdrN >= 1 && (s.hdr[0] == 0x7F || s.hdr[0] == 0xFF) {
			return 4
		}
		return 1
	}
	return 4
}

func (s *packetSplitter) payloadLen() int {
	if s.proto == ProtoAbridged {
		if s.hdr[0] == 0x7F || s.hdr[0] == 0xFF {
			return int(uint32(s.hdr[1])|uint32(s.hdr[2])<<8|uint32(s.hdr[3])<<16) * 4
		}
		return int(s.hdr[0]&0x7F) * 4
	}
	return int(binary.LittleEndian.Uint32(s.hdr[:]) & 0x7FFFFFFF)
}

func (s *packetSplitter) feed(plain []byte) {
	s.bounds = s.bounds[:0]
	if s.disabled {
		return
	}
	i := 0
	for i < len(plain) {
		if s.remain == 0 {
			for s.hdrN < s.headerLen() && i < len(plain) {
				s.hdr[s.hdrN] = plain[i]
				s.hdrN++
				i++
			}
			if s.hdrN < s.headerLen() {
				return
			}
			pl := s.payloadLen()
			if pl <= 0 {
				s.stopNow = true
				return
			}
			s.remain = pl
		}
		take := min(s.remain, len(plain)-i)
		i += take
		s.remain -= take
		if s.remain == 0 {
			s.hdrN = 0
			s.bounds = append(s.bounds, i)
		}
	}
}

// cut returns complete ciphertext packets for the chunk given to feed.
// Returned slices may alias cipher and are valid until the next call.
func (s *packetSplitter) cut(cipher []byte, out [][]byte) [][]byte {
	out = out[:0]
	if s.disabled {
		if len(cipher) > 0 {
			out = append(out, cipher)
		}
		return out
	}
	prev := 0
	for i, b := range s.bounds {
		if i == 0 && len(s.pending) > 0 {
			out = append(out, append(s.pending, cipher[:b]...))
			s.pending = nil
		} else {
			out = append(out, cipher[prev:b])
		}
		prev = b
	}
	if s.stopNow {
		// Same as Python: send the rest raw and stop splitting.
		rest := append(s.pending, cipher[prev:]...)
		s.pending = nil
		if len(rest) > 0 {
			out = append(out, rest)
		}
		s.disabled = true
		return out
	}
	if prev < len(cipher) {
		s.pending = append(s.pending, cipher[prev:]...)
	}
	return out
}

func (s *packetSplitter) flush() []byte {
	p := s.pending
	s.pending = nil
	return p
}
