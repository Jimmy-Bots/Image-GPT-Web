package register

import (
	cryptoRand "crypto/rand"
	"encoding/base64"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type RandomSource interface {
	Intn(n int) int
	Float64() float64
	Read(p []byte) (int, error)
}

type defaultRandomSource struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func newDefaultRandomSource() *defaultRandomSource {
	return &defaultRandomSource{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (r *defaultRandomSource) Intn(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rng.Intn(n)
}

func (r *defaultRandomSource) Float64() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rng.Float64()
}

func (r *defaultRandomSource) Read(p []byte) (int, error) {
	return cryptoRand.Read(p)
}

func randomID(src RandomSource, size int) string {
	if size < 8 {
		size = 8
	}
	buf := make([]byte, size)
	if _, err := src.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func randomUUID(src RandomSource) string {
	buf := make([]byte, 16)
	if _, err := src.Read(buf); err != nil {
		panic(err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		buf[0], buf[1], buf[2], buf[3],
		buf[4], buf[5],
		buf[6], buf[7],
		buf[8], buf[9],
		buf[10], buf[11], buf[12], buf[13], buf[14], buf[15],
	)
}
