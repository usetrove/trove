package report

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/usetrove/trove/internal/measure"
)

// FormatUSD rounds a dollar amount to what is worth showing: whole dollars from
// $100 up, cents below. Tiny amounts read "<$0.01" rather than a false zero.
func FormatUSD(v float64) string {
	switch {
	case v <= 0:
		return "$0"
	case v < 0.01:
		return "<$0.01"
	case v < 100:
		return "$" + commas(fmt.Sprintf("%.2f", v))
	}
	return "$" + commas(strconv.FormatInt(int64(math.Round(v)), 10))
}

// FormatTokens abbreviates a token count: 1.2M, 340K, 87.
func FormatTokens(v float64) string {
	switch {
	case v >= 1e9:
		return trimZero(fmt.Sprintf("%.1f", v/1e9)) + "B"
	case v >= 1e6:
		return trimZero(fmt.Sprintf("%.1f", v/1e6)) + "M"
	case v >= 1e4:
		return fmt.Sprintf("%.0fK", v/1e3)
	case v >= 1e3:
		return trimZero(fmt.Sprintf("%.1f", v/1e3)) + "K"
	}
	return fmt.Sprintf("%.0f", v)
}

// FormatPercent shows a whole percent; anything between 0 and 1 reads "<1%".
func FormatPercent(v float64) string {
	if v > 0 && v < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", v)
}

// Est marks an estimated dollar figure: "~$47". Measured figures get no prefix.
func Est(f measure.Figure) string {
	s := FormatUSD(f.Value)
	if f.Label == measure.Estimated && f.Value >= 0.01 {
		return "~" + s
	}
	return s
}

func trimZero(s string) string {
	if strings.HasSuffix(s, ".0") {
		return s[:len(s)-2]
	}
	return s
}

func commas(s string) string {
	whole, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if hasFrac {
		b.WriteString("." + frac)
	}
	return b.String()
}
