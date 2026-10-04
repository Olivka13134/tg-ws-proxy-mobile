package proxy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"net"
	"testing"
	"time"
)

// Build a random stream of transport packets, cut it into random chunks,
// run it through feed/cut and check every packet comes out whole and in order.
func TestSplitterRandomChunks(t *testing.T) {
	for _, proto := range []ProtoTag{ProtoAbridged, ProtoIntermediate, ProtoSecure} {
		for iter := 0; iter < 300; iter++ {
			var stream []byte
			var packets [][]byte
			for i := 0; i < 1+rand.IntN(40); i++ {
				var p []byte
				switch proto {
				case ProtoAbridged:
					words := 1 + rand.IntN(300)
					if rand.IntN(4) == 0 {
						words = 127 + rand.IntN(2000)
					}
					if words < 127 {
						p = append(p, byte(words))
					} else {
						p = append(p, 0x7f, byte(words), byte(words>>8), byte(words>>16))
					}
					p = append(p, randomBytes(words*4)...)
				default:
					n := 1 + rand.IntN(5000)
					var h [4]byte
					binary.LittleEndian.PutUint32(h[:], uint32(n))
					p = append(h[:], randomBytes(n)...)
				}
				packets = append(packets, p)
				stream = append(stream, p...)
			}
			// "cipher" = plaintext xor constant keystream, offsets preserved
			key := randomBytes(len(stream))
			cipher := make([]byte, len(stream))
			for i := range stream {
				cipher[i] = stream[i] ^ key[i]
			}
			sp := newPacketSplitter(proto)
			var got [][]byte
			var out [][]byte
			for off := 0; off < len(stream); {
				n := 1 + rand.IntN(9000)
				if rand.IntN(3) == 0 {
					n = 1 + rand.IntN(5)
				}
				end := min(off+n, len(stream))
				sp.feed(append([]byte(nil), stream[off:end]...))
				out = sp.cut(append([]byte(nil), cipher[off:end]...), out)
				for _, o := range out {
					got = append(got, append([]byte(nil), o...))
				}
				off = end
			}
			if tail := sp.flush(); len(tail) > 0 {
				t.Fatalf("proto %x: leftover %d bytes", proto, len(tail))
			}
			if len(got) != len(packets) {
				t.Fatalf("proto %x: got %d packets want %d", proto, len(got), len(packets))
			}
			off := 0
			for i, g := range got {
				want := cipher[off : off+len(packets[i])]
				if !bytes.Equal(g, want) {
					t.Fatalf("proto %x: packet %d mismatch", proto, i)
				}
				off += len(packets[i])
			}
		}
	}
}

func TestSplitterDisablesOnZeroLength(t *testing.T) {
	sp := newPacketSplitter(ProtoIntermediate)
	data := append([]byte{4, 0, 0, 0, 1, 2, 3, 4}, 0, 0, 0, 0, 9, 9)
	sp.feed(data)
	out := sp.cut(data, nil)
	if len(out) != 2 || len(out[0]) != 8 || len(out[1]) != 6 {
		t.Fatalf("unexpected split %v", out)
	}
	sp.feed([]byte{7, 7, 7})
	out = sp.cut([]byte{7, 7, 7}, out)
	if len(out) != 1 || len(out[0]) != 3 {
		t.Fatal("raw passthrough after disable")
	}
}

// The client builds an init with the secret; we must decode DC/proto and
// derive keys that interoperate with the client's own streams.
func TestHandshakeAndCryptoCtx(t *testing.T) {
	secret := randomBytes(16)
	rnd := randomBytes(64)
	binary.LittleEndian.PutUint32(rnd[56:], uint32(ProtoSecure))
	binary.LittleEndian.PutUint16(rnd[60:], uint16(0xFFFE)) // -2: media DC2
	h := sha256.Sum256(append(append([]byte(nil), rnd[8:40]...), secret...))
	cliEnc := newCTR(h[:], rnd[40:56])
	enc := make([]byte, 64)
	cliEnc.XORKeyStream(enc, rnd)
	init := append(append([]byte(nil), rnd[:56]...), enc[56:]...)

	hs, ok := parseClientHandshake(init, secret)
	if !ok || hs.DC != 2 || !hs.Media || hs.Tag != ProtoSecure {
		t.Fatalf("bad parse: %+v ok=%v", hs, ok)
	}
	relay := generateRelayInit(hs.Tag, -2)
	ctx := buildCryptoCtx(hs.PrekeyIV, secret, relay)

	// client -> proxy -> "telegram"
	msg := []byte("hello telegram, this is a test payload")
	wire := make([]byte, len(msg))
	cliEnc.XORKeyStream(wire, msg)
	ctx.cltDec.XORKeyStream(wire, wire)
	if !bytes.Equal(wire, msg) {
		t.Fatal("client stream decrypt mismatch")
	}
	ctx.tgEnc.XORKeyStream(wire, wire)
	// telegram side decodes relay init like a DC does
	tgDec := newCTR(relay[8:40], relay[40:56])
	full := make([]byte, 64)
	tgDec.XORKeyStream(full, relay)
	if ProtoTag(binary.LittleEndian.Uint32(full[56:])) != ProtoSecure || int16(binary.LittleEndian.Uint16(full[60:])) != -2 {
		t.Fatal("relay init does not carry tag/dc")
	}
	plain := make([]byte, len(wire))
	tgDec.XORKeyStream(plain, wire)
	if !bytes.Equal(plain, msg) {
		t.Fatal("telegram side decrypt mismatch")
	}
	// telegram -> proxy -> client
	r := reversed(relay[8:56])
	tgEnc := newCTR(r[:32], r[32:48])
	back := []byte("answer from DC")
	w2 := make([]byte, len(back))
	tgEnc.XORKeyStream(w2, back)
	ctx.tgDec.XORKeyStream(w2, w2)
	ctx.cltEnc.XORKeyStream(w2, w2)
	cr := reversed(rnd[8:56])
	h2 := sha256.Sum256(append(append([]byte(nil), cr[:32]...), secret...))
	cliDec := newCTR(h2[:], cr[32:48])
	cliDec.XORKeyStream(w2, w2)
	if !bytes.Equal(w2, back) {
		t.Fatal("client side decrypt mismatch")
	}
}

func TestWrongSecretRejected(t *testing.T) {
	init := randomBytes(64)
	ok := 0
	for i := 0; i < 200; i++ {
		if _, good := parseClientHandshake(randomBytes(64), randomBytes(16)); good {
			ok++
		}
	}
	_ = init
	if ok > 0 {
		t.Fatalf("%d random inits accepted", ok)
	}
}

func TestFakeTLSHello(t *testing.T) {
	secret := randomBytes(16)
	hello := make([]byte, 5+512)
	hello[0], hello[1], hello[2] = 0x16, 0x03, 0x01
	binary.BigEndian.PutUint16(hello[3:], 512)
	hello[5] = 0x01
	hello[43] = 0x20
	copy(hello[44:76], randomBytes(32))
	m := hmac.New(sha256.New, secret)
	m.Write(hello) // random still zero
	mac := m.Sum(nil)
	var ts [4]byte
	binary.LittleEndian.PutUint32(ts[:], uint32(time.Now().Unix()))
	for i := 0; i < 4; i++ {
		mac[28+i] ^= ts[i]
	}
	copy(hello[11:43], mac)
	cr, sid, ok := verifyClientHello(hello, secret)
	if !ok || !bytes.Equal(sid, hello[44:76]) {
		t.Fatal("valid hello rejected")
	}
	if _, _, bad := verifyClientHello(hello, randomBytes(16)); bad {
		t.Fatal("wrong secret accepted")
	}
	sh := buildServerHello(secret, cr, sid)
	m2 := hmac.New(sha256.New, secret)
	zeroed := append([]byte(nil), sh...)
	copy(zeroed[11:43], make([]byte, 32))
	m2.Write(cr)
	m2.Write(zeroed)
	if !bytes.Equal(m2.Sum(nil), sh[11:43]) {
		t.Fatal("server hello digest mismatch")
	}
}

var pyDecoded = []string{"pclead.co.uk", "offshor.co.uk", "cakeisalie.co.uk", "noskomnadzor.co.uk", "lovetrue.co.uk", "sorokdva.co.uk", "pyatdesyatdva.co.uk", "kartoshka.co.uk", "sorokodin.co.uk", "pyatdesyatodin.co.uk", "notelega.co.uk", "ebally.co.uk", "nebally.co.uk", "havegreatday.co.uk", "pomogite.co.uk", "fixtelega.co.uk", "sadnews.co.uk", "onedaychamp.co.uk", "stopblocking.co.uk", "nothingthere.co.uk"}

// must match Python's _dd() exactly
func TestDecodeCFDomain(t *testing.T) {
	got := defaultCFDomains()
	for i := range pyDecoded {
		if got[i] != pyDecoded[i] {
			t.Fatalf("%d: %q != %q", i, got[i], pyDecoded[i])
		}
	}
}

func TestMaskXOR(t *testing.T) {
	for n := 0; n < 70; n++ {
		src := randomBytes(n)
		var k [4]byte
		copy(k[:], randomBytes(4))
		dst := make([]byte, n)
		maskXOR(dst, src, k)
		for i := range src {
			if dst[i] != src[i]^k[i&3] {
				t.Fatalf("n=%d i=%d", n, i)
			}
		}
	}
}

// A dead listening socket (as when iOS reclaims it) must be recreated.
func TestListenerRecovers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Port = 18443
	cfg.PoolSize = 0
	cfg.CFProxy = false
	cfg.DCRedirects = map[int]string{}
	s := NewServer(cfg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	s.listener().Close() // simulate the OS killing the socket
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", "127.0.0.1:18443"); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("listener was not recreated")
}
