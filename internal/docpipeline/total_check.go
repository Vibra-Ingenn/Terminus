package docpipeline

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// A misread digit in a photo ("$128.03" read as "$328.03") gives a total
// that looks fine and is wrong. Bills add up, so the amounts above the
// total can catch it: either subtotal + tax, or the sum of each line's last
// amount. When neither matches and one is a single digit away from the
// total, the total is almost certainly misread.

var amountRe = regexp.MustCompile(`\$?\s?(\d{1,3}(?:,\d{3})*\.\d{2}|\d+\.\d{2})\b`)
var subtotalLine = regexp.MustCompile(`(?i)\bsub[\s-]?total\b`)
var totalLine = regexp.MustCompile(`(?i)\btotal\b|\bamount\s+due\b|\bbalance\s+due\b`)

// CheckTotalArithmetic returns a warning when the total does not match
// the amounts listed above it but differs by a single digit from what they
// add up to. It returns "" when the total checks out or cannot be checked.
func CheckTotalArithmetic(text, total string) string {
	want, ok := parseAmount(total)
	if !ok {
		return ""
	}
	lines := strings.Split(text, "\n")
	totalAt := -1
	for i, l := range lines {
		if totalLine.MatchString(l) && !subtotalLine.MatchString(l) && strings.Contains(strings.ReplaceAll(l, ",", ""), strings.ReplaceAll(total, ",", "")) {
			totalAt = i
			break
		}
	}
	if totalAt < 0 {
		return ""
	}

	var sums []float64
	// Every line's last amount above the total, skipping a subtotal line.
	var items, sub, afterSub float64
	haveSub := false
	n := 0
	for _, l := range lines[:totalAt] {
		m := amountRe.FindAllStringSubmatch(l, -1)
		if len(m) == 0 {
			continue
		}
		v, ok := parseAmount(m[len(m)-1][1])
		if !ok {
			continue
		}
		if subtotalLine.MatchString(l) {
			sub, haveSub, afterSub = v, true, 0
			continue
		}
		if haveSub {
			afterSub += v
		} else {
			items += v
		}
		n++
	}
	if n >= 2 || haveSub {
		sums = append(sums, items+afterSub)
	}
	if haveSub {
		sums = append(sums, sub+afterSub)
	}
	if len(sums) == 0 {
		return ""
	}
	for _, s := range sums {
		if math.Abs(s-want) < 0.005 {
			return ""
		}
	}
	for _, s := range sums {
		alt := strconv.FormatFloat(s, 'f', 2, 64)
		if oneDigitApart(alt, strconv.FormatFloat(want, 'f', 2, 64)) {
			return fmt.Sprintf("the total (%s) and the amounts above it (which add up to %s) differ by one digit, so one of them was probably misread from the photo; check them against the original", total, alt)
		}
	}
	return ""
}

func parseAmount(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(s), "$"), ",", ""), 64)
	return v, err == nil
}

func oneDigitApart(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	diff := 0
	for i := range a {
		if a[i] != b[i] {
			diff++
		}
	}
	return diff == 1
}
