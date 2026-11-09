package docpipeline

import (
	"regexp"
	"strings"
	"unicode"
)

// OCR from a phone photo or a bad scan usually gets the words almost right:
// "TOTAL OVE" for "TOTAL DUE", "Bilt to" for "Bill to", "invorwe" for
// "invoice". The field patterns need the exact label, so one wrong letter
// used to lose the whole field. RepairOCRText puts the labels back before
// the patterns run.
//
// It only touches the words a bill uses as labels, and only when the fix is
// unambiguous, so the text people read (names, items, addresses) is kept as
// OCR read it. Values are never invented: a misread number stays misread,
// except for letters that OCR commonly puts inside a price ("$38.5O").

// labelWords are fixed when a word is close to one of them.
// Words of 4–6 letters may be one edit away, longer words two.
var labelWords = []string{
	"invoice", "receipt", "subtotal", "total", "amount", "balance",
	"customer", "billed", "statement", "payment", "issued", "dated",
	"vendor", "merchant", "charged", "number", "account", "grand",
	"bill", "sold", "date", "terms",
}

// realWords are ordinary words one letter away from a label. They are left
// alone: "Rate" on an invoice is a rate, not a misread "Date".
var realWords = map[string]bool{
	"rate": true, "late": true, "gate": true, "data": true, "dale": true,
	"fate": true, "mate": true, "hate": true, "sate": true, "dare": true,
	"dame": true, "dote": true, "dates": true, "rated": true, "bills": true,
	"pill": true, "will": true, "fill": true, "hill": true, "kill": true,
	"mill": true, "till": true, "sill": true, "gill": true, "bull": true,
	"ball": true, "bell": true, "belt": true, "built": true, "bile": true,
	"bold": true, "sole": true, "solo": true, "soda": true,
	"told": true, "cold": true, "fold": true, "gold": true, "hold": true,
	"mold": true, "role": true, "soul": true, "salad": true, "solid": true,
	"tonal": true, "tidal": true, "team": true,
	"teams": true, "germs": true, "terns": true, "tears": true, "grant": true,
	"grind": true, "grade": true, "gland": true, "brand": true, "bland": true,
	"vender": true, "mounts": true, "counts": true, "amounts": true,
	"lumber": true, "member": true,
}

// shortLabelNeedsContext: a 4–5 letter label is only fixed where it sits in
// label position, followed by ':' '#' a digit or '$' (or by "to" for
// "bill"/"sold"), so a stray word in a sentence is not rewritten.
var labelTail = regexp.MustCompile(`^(?:[’'‘.,;]{1,2}[ \t]*[\d$]|[ \t]*(?:[:#$]|\d|to\b|due\b|no\b))`)

// misreadColon is punctuation OCR puts where a label's colon was
// ("Tota’. 134.00"), followed by the value.
var misreadColon = regexp.MustCompile(`^[’'‘.,;]{1,2}([ \t]*[\d$])`)

var wordRe = regexp.MustCompile(`[A-Za-z]{3,}`)

// dueRe fixes the three-letter "due" after total/amount/balance, where OCR
// often reads "OVE", "DLE", "0UE" or "DIJE".
var dueRe = regexp.MustCompile(`(?i)\b(total|amount|balance)([ \t]+)([A-Za-z0-9]{2,4})\b`)

// moneyRe finds price-like tokens that may hold OCR letters: "$38.5O",
// "$l54.00", "S12.99" is left alone (no '$' and starts with a letter).
var moneyRe = regexp.MustCompile(`\$[ \t]?[0-9OoIlSsB][0-9OoIlSsB,]*\.[ \t]?[0-9OoIlSsB]{2}\b`)

// RepairOCRText fixes label words and price characters in OCR output.
// It returns the repaired text and how many fixes it made.
func RepairOCRText(text string) (string, int) {
	fixes := 0
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = repairLine(line, &fixes)
	}
	out := strings.Join(lines, "\n")

	out = dueRe.ReplaceAllStringFunc(out, func(m string) string {
		sm := dueRe.FindStringSubmatch(m)
		word := sm[3]
		if strings.EqualFold(word, "due") || !looksLikeDue(word) {
			return m
		}
		fixes++
		return sm[1] + sm[2] + matchCase("due", word)
	})

	out = moneyRe.ReplaceAllStringFunc(out, func(m string) string {
		fixed := strings.Map(func(r rune) rune {
			switch r {
			case 'O', 'o':
				return '0'
			case 'I', 'l':
				return '1'
			case 'S', 's':
				return '5'
			case 'B':
				return '8'
			}
			return r
		}, m)
		fixed = strings.ReplaceAll(fixed, ". ", ".")
		if fixed != m {
			fixes++
		}
		return fixed
	})
	return out, fixes
}

// Stray specks OCR reads at the edges of a line (" :", "= ", "} ") break
// patterns that expect a name to end the line, like the vendor on line one.
var (
	lineTailJunk = regexp.MustCompile(`(?:[ \t]+[^\w\s$%)#]{1,2})+[ \t]*$`)
	lineHeadJunk = regexp.MustCompile(`^[ \t]*(?:[^\w\s$(#]{1,2}[ \t]+)+`)
	// "$422 99": the decimal point read as a space.
	spacedCents = regexp.MustCompile(`(\$[ \t]?\d{1,3}(?:,\d{3})*) (\d{2})\b([^.,\d]|$)`)
)

func trimLineJunk(line string) string {
	t := lineTailJunk.ReplaceAllString(line, "")
	if strings.TrimSpace(t) == "" {
		return line
	}
	h := lineHeadJunk.ReplaceAllString(t, "")
	if strings.TrimSpace(h) == "" {
		return t
	}
	return h
}

func repairLine(line string, fixes *int) string {
	if t := trimLineJunk(line); t != line {
		line = t
		*fixes++
	}
	if t := spacedCents.ReplaceAllString(line, "$1.$2$3"); t != line {
		line = t
		*fixes++
	}
	locs := wordRe.FindAllStringIndex(line, -1)
	if len(locs) == 0 {
		return line
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		word := line[loc[0]:loc[1]]
		b.WriteString(line[last:loc[0]])
		last = loc[1]
		rest := line[loc[1]:]
		fixed := fixLabelWord(word, rest)
		if fixed != word {
			*fixes++
		}
		b.WriteString(fixed)
		if isLabelWord(fixed) {
			if m := misreadColon.FindStringSubmatchIndex(rest); m != nil {
				b.WriteString(":")
				last = loc[1] + m[2]
				*fixes++
			}
		}
	}
	b.WriteString(line[last:])
	return b.String()
}

func isLabelWord(w string) bool {
	lower := strings.ToLower(w)
	for _, label := range labelWords {
		if lower == label {
			return true
		}
	}
	return false
}

func fixLabelWord(word, rest string) string {
	lower := strings.ToLower(word)
	if realWords[lower] {
		return word
	}
	for _, label := range labelWords {
		// The label itself, or its plural ("invoices", "totals").
		if lower == label || lower == label+"s" {
			return word
		}
	}
	best, bestDist, tie := "", 99, false
	for _, label := range labelWords {
		max := 1
		if len(label) >= 7 {
			max = 2
		}
		if abs(len(label)-len(lower)) > max {
			continue
		}
		d := editDistance(lower, label)
		if d > max {
			continue
		}
		if d < bestDist {
			best, bestDist, tie = label, d, false
		} else if d == bestDist {
			tie = true
		}
	}
	if best == "" || tie {
		return word
	}
	// The first letter is the one OCR gets right most often; a different
	// first letter on a short word is more likely a different word.
	if len(best) <= 6 && lower[0] != best[0] {
		return word
	}
	if len(best) <= 5 && !labelTail.MatchString(rest) {
		return word
	}
	return matchCase(best, word)
}

func looksLikeDue(w string) bool {
	// Common misreads of DUE: OVE, 0UE, DLE, DUF, DIJE, QUE.
	u := strings.NewReplacer("IJ", "U", "LI", "U").Replace(strings.ToUpper(w))
	if len(u) != 3 {
		return false
	}
	return strings.ContainsRune("DO0Q", rune(u[0])) &&
		strings.ContainsRune("UVLJ", rune(u[1])) &&
		strings.ContainsRune("EFC", rune(u[2]))
}

// matchCase spells label the way word was written: TOTAL, Total or total.
func matchCase(label, word string) string {
	letters := 0
	upper := 0
	for _, r := range word {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	switch {
	// Mostly capitals counts as capitals: OCR often drops one ("INVOlCE").
	case letters >= 2 && upper*2 > letters:
		return strings.ToUpper(label)
	case len(word) > 0 && unicode.IsUpper(rune(word[0])):
		return strings.ToUpper(label[:1]) + label[1:]
	default:
		return label
	}
}

// editDistance is the Levenshtein distance with adjacent swaps (OCR often
// swaps two letters) counted as one edit.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = minInt(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = minInt(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

func minInt(xs ...int) int {
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
