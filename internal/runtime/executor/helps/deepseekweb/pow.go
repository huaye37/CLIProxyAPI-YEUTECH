// Protocol and hash reference: OmniRoute v3.8.51 (MIT); see THIRD_PARTY_NOTICES_YEUTECH.md.
package deepseekweb

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"
	"strconv"
)

type Challenge struct {
	Algorithm  string `json:"algorithm"`
	Challenge  string `json:"challenge"`
	Salt       string `json:"salt"`
	Difficulty int    `json:"difficulty"`
	ExpireAt   int64  `json:"expire_at"`
	Signature  string `json:"signature"`
	TargetPath string `json:"target_path"`
}

var rotations = [25]int{0, 1, 62, 28, 27, 36, 44, 6, 55, 20, 3, 10, 43, 25, 39, 41, 45, 15, 21, 8, 18, 2, 61, 56, 14}
var rounds = [24]uint64{0x1, 0x8082, 0x800000000000808a, 0x8000000080008000, 0x808b, 0x80000001, 0x8000000080008081, 0x8000000000008009, 0x8a, 0x88, 0x80008009, 0x8000000a, 0x8000808b, 0x800000000000008b, 0x8000000000008089, 0x8000000000008003, 0x8000000000008002, 0x8000000000000080, 0x800a, 0x800000008000000a, 0x8000000080008081, 0x8000000000008080, 0x80000001, 0x8000000080008008}

func permute(a *[25]uint64) {
	for _, rc := range rounds[1:] {
		var c [5]uint64
		var b [25]uint64
		for x := 0; x < 5; x++ {
			c[x] = a[x] ^ a[x+5] ^ a[x+10] ^ a[x+15] ^ a[x+20]
		}
		for y := 0; y < 5; y++ {
			for x := 0; x < 5; x++ {
				a[x+5*y] ^= c[(x+4)%5] ^ bits.RotateLeft64(c[(x+1)%5], 1)
			}
		}
		for y := 0; y < 5; y++ {
			for x := 0; x < 5; x++ {
				b[y+5*((2*x+3*y)%5)] = bits.RotateLeft64(a[x+5*y], rotations[x+5*y])
			}
		}
		for y := 0; y < 5; y++ {
			for x := 0; x < 5; x++ {
				a[x+5*y] = b[x+5*y] ^ (^b[(x+1)%5+5*y] & b[(x+2)%5+5*y])
			}
		}
		a[0] ^= rc
	}
}
func hash(input string) [32]byte {
	var a [25]uint64
	data := []byte(input)
	for len(data) >= 136 {
		for i := 0; i < 17; i++ {
			a[i] ^= binary.LittleEndian.Uint64(data[i*8:])
		}
		permute(&a)
		data = data[136:]
	}
	var tail [136]byte
	copy(tail[:], data)
	tail[len(data)] = 6
	tail[135] |= 128
	for i := 0; i < 17; i++ {
		a[i] ^= binary.LittleEndian.Uint64(tail[i*8:])
	}
	permute(&a)
	var out [32]byte
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(out[i*8:], a[i])
	}
	return out
}
func Solve(ctx context.Context, c Challenge) (string, error) {
	target, err := hex.DecodeString(c.Challenge)
	if err != nil || len(target) != 32 || c.Algorithm != "DeepSeekHashV1" || len(c.Salt) == 0 || len(c.Salt) > 1024 || c.Difficulty < 1 || c.Difficulty > 250000 || c.ExpireAt <= 0 || c.TargetPath != "/api/v0/chat/completion" {
		return "", fmt.Errorf("unsupported DeepSeek challenge")
	}
	prefix := c.Salt + "_" + strconv.FormatInt(c.ExpireAt, 10) + "_"
	for n := 0; n < c.Difficulty; n++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		sum := hash(prefix + strconv.Itoa(n))
		if string(sum[:]) != string(target) {
			continue
		}
		raw, err := json.Marshal(map[string]any{"algorithm": c.Algorithm, "challenge": c.Challenge, "salt": c.Salt, "answer": n, "signature": c.Signature, "target_path": c.TargetPath})
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(raw), nil
	}
	return "", fmt.Errorf("DeepSeek challenge has no solution within its limit")
}
