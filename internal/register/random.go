package register

import (
	cryptoRand "crypto/rand"
	"encoding/base64"
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
