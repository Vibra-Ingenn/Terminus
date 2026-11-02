package pdf

// AES-256 encryption (Standard security handler, V=5, R=5 or R=6), as in
// ISO 32000-2 §7.6.4.3.3-4. Added to this copy of the library: the original
// rejected these files as "malformed PDF: 256-bit encryption key".

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
)

func (r *Reader) initEncryptV5(encrypt dict, password string) error {
	R, _ := encrypt["R"].(int64)
	if R != 5 && R != 6 {
		return fmt.Errorf("unsupported PDF: encryption revision R=%d", R)
	}
	O, _ := encrypt["O"].(string)
	U, _ := encrypt["U"].(string)
	OE, _ := encrypt["OE"].(string)
	UE, _ := encrypt["UE"].(string)
	if len(O) < 48 || len(U) < 48 || len(OE) != 32 || len(UE) != 32 {
		return fmt.Errorf("malformed PDF: missing O, U, OE or UE encryption parameters")
	}
	pw := []byte(password)
	if len(pw) > 127 {
		pw = pw[:127]
	}
	u, o := []byte(U[:48]), []byte(O[:48])

	var key []byte
	switch {
	case bytes.Equal(hashV5(R, pw, u[32:40], nil), u[:32]):
		key = aesNoPadDecrypt(hashV5(R, pw, u[40:48], nil), []byte(UE))
	case bytes.Equal(hashV5(R, pw, o[32:40], u), o[:32]):
		key = aesNoPadDecrypt(hashV5(R, pw, o[40:48], u), []byte(OE))
	default:
		return ErrInvalidPassword
	}
	if key == nil {
		return fmt.Errorf("malformed PDF: bad AES-256 key")
	}
	r.key = key
	r.useAES = true
	return nil
}

// aesNoPadDecrypt decrypts a 32-byte file key with AES-256-CBC, a zero IV and
// no padding.
func aesNoPadDecrypt(k, data []byte) []byte {
	block, err := aes.NewCipher(k)
	if err != nil || len(data)%aes.BlockSize != 0 {
		return nil
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, data)
	return out
}

// hashV5 is Algorithm 2.A's hash for R=5 (plain SHA-256) and Algorithm 2.B for R=6.
func hashV5(R int64, pw, salt, udata []byte) []byte {
	h := sha256.New()
	h.Write(pw)
	h.Write(salt)
	h.Write(udata)
	k := h.Sum(nil)
	if R == 5 {
		return k
	}
	var e []byte
	for i := 0; i < 64 || int(e[len(e)-1]) > i-32; i++ {
		k1 := make([]byte, 0, 64*(len(pw)+len(k)+len(udata)))
		for j := 0; j < 64; j++ {
			k1 = append(k1, pw...)
			k1 = append(k1, k...)
			k1 = append(k1, udata...)
		}
		block, _ := aes.NewCipher(k[:16])
		e = make([]byte, len(k1))
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1)
		// The first 16 bytes of E as a big number, mod 3. 256 mod 3 is 1, so
		// summing the bytes gives the same remainder.
		sum := 0
		for _, b := range e[:16] {
			sum += int(b)
		}
		var hh hash.Hash
		switch sum % 3 {
		case 0:
			hh = sha256.New()
		case 1:
			hh = sha512.New384()
		default:
			hh = sha512.New()
		}
		hh.Write(e)
		k = hh.Sum(nil)
	}
	return k[:32]
}
