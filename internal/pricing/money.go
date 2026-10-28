package pricing

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// Dec is an exact decimal number (never a float). Prices are held as Dec so that
// hand-calculated test expectations are exact and rounding is always explicit.
type Dec struct{ r *big.Rat }

// ParseDec parses a plain decimal such as "12.50".
func ParseDec(s string) (Dec, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok || strings.ContainsAny(s, "eE/") {
		return Dec{}, fmt.Errorf("%q is not a plain decimal number", s)
	}
	return Dec{r}, nil
}

// MustDec is ParseDec for literals in code and tests.
func MustDec(s string) Dec {
	d, err := ParseDec(s)
	if err != nil {
		panic(err)
	}
	return d
}

// UnmarshalJSON accepts a JSON string ("0.30") so no float ever touches a price.
func (d *Dec) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("prices must be written as JSON strings like \"0.30\": %w", err)
	}
	v, err := ParseDec(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// MarshalJSON writes the exact decimal as a string.
func (d Dec) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// IsSet reports whether d holds a value.
func (d Dec) IsSet() bool { return d.r != nil }

// Sign returns -1, 0 or +1. An unset Dec is 0.
func (d Dec) Sign() int {
	if d.r == nil {
		return 0
	}
	return d.r.Sign()
}

// Cmp compares two set values.
func (d Dec) Cmp(o Dec) int { return d.r.Cmp(o.r) }

// String is the exact decimal with trailing zeros trimmed.
func (d Dec) String() string {
	if d.r == nil {
		return ""
	}
	return trimRat(d.r)
}

func trimRat(r *big.Rat) string {
	s := r.FloatString(12)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

// Money is an exact USD amount.
type Money struct{ r *big.Rat }

func zeroMoney() Money { return Money{new(big.Rat)} }

// MoneyFromString builds a Money from a decimal literal (tests, golden files).
func MoneyFromString(s string) Money { return Money{MustDec(s).r} }

func (m Money) rat() *big.Rat {
	if m.r == nil {
		return new(big.Rat)
	}
	return m.r
}

// Sign returns -1, 0 or +1.
func (m Money) Sign() int { return m.rat().Sign() }

// Cmp compares two amounts.
func (m Money) Cmp(o Money) int { return m.rat().Cmp(o.rat()) }

// Add returns m+o.
func (m Money) Add(o Money) Money { return Money{new(big.Rat).Add(m.rat(), o.rat())} }

// Sub returns m-o.
func (m Money) Sub(o Money) Money { return Money{new(big.Rat).Sub(m.rat(), o.rat())} }

// String is the exact amount in USD with trailing zeros trimmed ("0.0015").
func (m Money) String() string { return trimRat(m.rat()) }

// Float64 is for display only; never feed it back into pricing.
func (m Money) Float64() float64 { f, _ := m.rat().Float64(); return f }

// MarshalJSON writes the exact amount as a string.
func (m Money) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

// callsAt prices n tool calls at a USD-per-1000-calls rate.
func callsAt(per1000 Dec, n int64) *big.Rat {
	v := new(big.Rat).Mul(per1000.r, new(big.Rat).SetInt64(n))
	return v.Quo(v, big.NewRat(1000, 1))
}

// tokensAt prices n units at a USD-per-million rate.
func tokensAt(perMTok Dec, n int64) *big.Rat {
	v := new(big.Rat).Mul(perMTok.r, new(big.Rat).SetInt64(n))
	return v.Quo(v, big.NewRat(1_000_000, 1))
}
