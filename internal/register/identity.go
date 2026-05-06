package register

import (
	"fmt"
	"strings"
)

type IdentityGenerator interface {
	Password() string
	Name() (string, string)
	Birthdate() string
}

type defaultIdentityGenerator struct {
	random RandomSource
}

func newDefaultIdentityGenerator(random RandomSource) defaultIdentityGenerator {
	return defaultIdentityGenerator{random: random}
}

func (g defaultIdentityGenerator) Password() string {
	upper := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lower := "abcdefghijklmnopqrstuvwxyz"
	digits := "0123456789"
	symbols := "!@#$%"
	all := upper + lower + digits + symbols
	out := []byte{
		upper[g.random.Intn(len(upper))],
		lower[g.random.Intn(len(lower))],
		digits[g.random.Intn(len(digits))],
		symbols[g.random.Intn(len(symbols))],
	}
	for len(out) < 16 {
		out = append(out, all[g.random.Intn(len(all))])
	}
	for i := len(out) - 1; i > 0; i-- {
		j := g.random.Intn(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func (g defaultIdentityGenerator) Name() (string, string) {
	firsts := []string{"James", "Robert", "John", "Michael", "David", "Mary", "Emma", "Olivia"}
	lasts := []string{"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller"}
	return firsts[g.random.Intn(len(firsts))], lasts[g.random.Intn(len(lasts))]
}

func (g defaultIdentityGenerator) Birthdate() string {
	year := 1996 + g.random.Intn(11)
	month := 1 + g.random.Intn(12)
	day := 1 + g.random.Intn(28)
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func joinFullName(first string, last string) string {
	return strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
}
