package proxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

const (
	handshakeLen = 64
	skipLen      = 8
	prekeyLen    = 32
	ivLen        = 16
	protoTagPos  = 56
	dcIdxPos     = 60
)

type ProtoTag uint32

const (
	ProtoAbridged     ProtoTag = 0xEFEFEFEF
	ProtoIntermediate ProtoTag = 0xEEEEEEEE
	ProtoSecure       ProtoTag = 0xDDDDDDDD // padded intermediate
)

var DCDefaultIPs = map[int]string{
	1:   "149.154.175.50",
	2:   "149.154.167.51",
	3:   "149.154.175.100",
	4:   "149.154.167.91",
	5:   "149.154.171.5",
	203: "91.105.192.100",
}

var DCTestIPs = map[int]string{
	1: "149.154.175.10",
	2: "149.154.167.40",
	3: "149.154.175.117",
}

const (
	wsPath     = "/apiws"
	wsPathTest = "/apiws_test"
)

func wsDomains(dc int, media bool) []string {
	if dc == 203 {
		dc = 2
	}
	a := fmt.Sprintf("kws%d.web.telegram.org", dc)
	b := fmt.Sprintf("kws%d-1.web.telegram.org", dc)
	if media {
		return []string{b, a}
	}
	return []string{a, b}
}

func newCTR(key, iv []byte) cipher.Stream {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // key length is always 32
	}
	return cipher.NewCTR(block, iv)
}

var zero64 [64]byte

func skip64(s cipher.Stream) {
	var tmp [64]byte
	s.XORKeyStream(tmp[:], zero64[:])
}

type clientHandshake struct {
	DC       int
	Media    bool
	Tag      ProtoTag
	PrekeyIV []byte // 48 bytes: handshake[8:56]
}

// parseClientHandshake decrypts the 64-byte obfuscated2 init from a client
// that uses the proxy secret.
func parseClientHandshake(hs, secret []byte) (*clientHandshake, bool) {
	if len(hs) != handshakeLen {
		return nil, false
	}
	prekeyIV := hs[skipLen : skipLen+prekeyLen+ivLen]
	h := sha256.New()
	h.Write(prekeyIV[:prekeyLen])
	h.Write(secret)
	key := h.Sum(nil)
	dec := make([]byte, handshakeLen)
	newCTR(key, prekeyIV[prekeyLen:]).XORKeyStream(dec, hs)

	tag := ProtoTag(binary.LittleEndian.Uint32(dec[protoTagPos:]))
	if tag != ProtoAbridged && tag != ProtoIntermediate && tag != ProtoSecure {
		return nil, false
	}
	dcIdx := int16(binary.LittleEndian.Uint16(dec[dcIdxPos:]))
	dc := int(dcIdx)
	media := dc < 0
	if media {
		dc = -dc
	}
	return &clientHandshake{
		DC:       dc,
		Media:    media,
		Tag:      tag,
		PrekeyIV: append([]byte(nil), prekeyIV...),
	}, true
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

var reservedStarts = [][4]byte{
	{'H', 'E', 'A', 'D'}, {'P', 'O', 'S', 'T'}, {'G', 'E', 'T', ' '},
	{0xee, 0xee, 0xee, 0xee}, {0xdd, 0xdd, 0xdd, 0xdd}, {0x16, 0x03, 0x01, 0x02},
}

// generateRelayInit builds the obfuscated2 init we send to Telegram
// (no secret on this leg, raw key/iv taken from the random bytes).
func generateRelayInit(tag ProtoTag, dcIdx int16) []byte {
	var rnd []byte
	for {
		rnd = randomBytes(handshakeLen)
		if rnd[0] == 0xEF {
			continue
		}
		bad := false
		for _, r := range reservedStarts {
			if rnd[0] == r[0] && rnd[1] == r[1] && rnd[2] == r[2] && rnd[3] == r[3] {
				bad = true
				break
			}
		}
		if bad || (rnd[4]|rnd[5]|rnd[6]|rnd[7]) == 0 {
			continue
		}
		break
	}
	plain := append([]byte(nil), rnd...)
	binary.LittleEndian.PutUint32(plain[protoTagPos:], uint32(tag))
	binary.LittleEndian.PutUint16(plain[dcIdxPos:], uint16(dcIdx))
	enc := make([]byte, handshakeLen)
	newCTR(rnd[skipLen:skipLen+prekeyLen], rnd[skipLen+prekeyLen:skipLen+prekeyLen+ivLen]).
		XORKeyStream(enc, plain)
	out := append([]byte(nil), rnd[:protoTagPos]...)
	return append(out, enc[protoTagPos:]...)
}

type cryptoCtx struct {
	cltDec cipher.Stream // decrypt from client
	cltEnc cipher.Stream // encrypt to client
	tgEnc  cipher.Stream // encrypt to telegram
	tgDec  cipher.Stream // decrypt from telegram
}

func reversed(b []byte) []byte {
	r := make([]byte, len(b))
	for i := range b {
		r[len(b)-1-i] = b[i]
	}
	return r
}

func buildCryptoCtx(prekeyIV, secret, relayInit []byte) *cryptoCtx {
	hk := func(prekey []byte) []byte {
		h := sha256.New()
		h.Write(prekey)
		h.Write(secret)
		return h.Sum(nil)
	}
	cltDec := newCTR(hk(prekeyIV[:prekeyLen]), prekeyIV[prekeyLen:])
	rev := reversed(prekeyIV)
	cltEnc := newCTR(hk(rev[:prekeyLen]), rev[prekeyLen:])
	skip64(cltDec)

	relayKI := relayInit[skipLen : skipLen+prekeyLen+ivLen]
	tgEnc := newCTR(relayKI[:prekeyLen], relayKI[prekeyLen:])
	relayRev := reversed(relayKI)
	tgDec := newCTR(relayRev[:prekeyLen], relayRev[prekeyLen:])
	skip64(tgEnc)

	return &cryptoCtx{cltDec: cltDec, cltEnc: cltEnc, tgEnc: tgEnc, tgDec: tgDec}
}
