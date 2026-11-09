package tools

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"terminus/internal/docpipeline"

	_ "image/gif"
	_ "image/jpeg"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Phone photos of bills are tilted, unevenly lit and grainy. Tesseract
// splits ink from paper with one global threshold, so a grey page on a dark
// table loses its text, and lines tilted more than a few degrees fall apart.
//
// cleanPhotoForOCR fixes the three things that matter most, in plain Go:
//  1. light denoise (3×3 median) to take out sensor grain,
//  2. straighten: find the angle where the ink lines up into rows,
//  3. even out the light: divide by the paper's own brightness so the page
//     turns white and the text stays dark, whatever the lighting.
//
// On test photos tilted 8–11°, dim and grainy, this took the bill words OCR
// recovered from 13 to 41.

// grayImg is a float grey image, 0 = black, 255 = white.
type grayImg struct {
	w, h int
	px   []float32
}

func (g *grayImg) at(x, y int) float32 { return g.px[y*g.w+x] }

func toGray(img image.Image) *grayImg {
	b := img.Bounds()
	g := &grayImg{w: b.Dx(), h: b.Dy(), px: make([]float32, b.Dx()*b.Dy())}
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			c := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
			g.px[y*g.w+x] = float32(c.Y)
		}
	}
	return g
}

// median3 is a 3×3 median filter.
func median3(g *grayImg) *grayImg {
	out := &grayImg{w: g.w, h: g.h, px: make([]float32, len(g.px))}
	var win [9]float32
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			n := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := clampInt(x+dx, 0, g.w-1), clampInt(y+dy, 0, g.h-1)
					win[n] = g.at(xx, yy)
					n++
				}
			}
			s := win[:]
			sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
			out.px[y*g.w+x] = win[4]
		}
	}
	return out
}

// boxMean blurs with a (2r+1)² box, using a summed-area table.
func boxMean(g *grayImg, r int) *grayImg {
	w, h := g.w, g.h
	sat := make([]float64, (w+1)*(h+1))
	for y := 0; y < h; y++ {
		var row float64
		for x := 0; x < w; x++ {
			row += float64(g.at(x, y))
			sat[(y+1)*(w+1)+x+1] = sat[y*(w+1)+x+1] + row
		}
	}
	out := &grayImg{w: w, h: h, px: make([]float32, len(g.px))}
	for y := 0; y < h; y++ {
		y0, y1 := clampInt(y-r, 0, h-1), clampInt(y+r, 0, h-1)+1
		for x := 0; x < w; x++ {
			x0, x1 := clampInt(x-r, 0, w-1), clampInt(x+r, 0, w-1)+1
			sum := sat[y1*(w+1)+x1] - sat[y0*(w+1)+x1] - sat[y1*(w+1)+x0] + sat[y0*(w+1)+x0]
			out.px[y*w+x] = float32(sum / float64((x1-x0)*(y1-y0)))
		}
	}
	return out
}

// maxFilter takes the brightest pixel in a (2r+1)² window (separable), which
// wipes text strokes out and leaves the paper's brightness.
func maxFilter(g *grayImg, r int) *grayImg {
	tmp := &grayImg{w: g.w, h: g.h, px: make([]float32, len(g.px))}
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			m := float32(0)
			for k := clampInt(x-r, 0, g.w-1); k <= clampInt(x+r, 0, g.w-1); k++ {
				if v := g.at(k, y); v > m {
					m = v
				}
			}
			tmp.px[y*g.w+x] = m
		}
	}
	out := &grayImg{w: g.w, h: g.h, px: make([]float32, len(g.px))}
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			m := float32(0)
			for k := clampInt(y-r, 0, g.h-1); k <= clampInt(y+r, 0, g.h-1); k++ {
				if v := tmp.at(x, k); v > m {
					m = v
				}
			}
			out.px[y*g.w+x] = m
		}
	}
	return out
}

func halfSize(g *grayImg) *grayImg {
	w, h := g.w/2, g.h/2
	out := &grayImg{w: w, h: h, px: make([]float32, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.px[y*w+x] = (g.at(2*x, 2*y) + g.at(2*x+1, 2*y) + g.at(2*x, 2*y+1) + g.at(2*x+1, 2*y+1)) / 4
		}
	}
	return out
}

// estimateSkew returns the tilt in degrees (counter-clockwise positive) of
// the text. Ink is found by local contrast, not a fixed level, so a dark
// table around the page is not mistaken for text. For each candidate angle
// the ink is projected onto rows; text lines line up into sharp peaks at
// the right angle, which maximises the sum of squared row counts.
func estimateSkew(g *grayImg) float64 {
	s := halfSize(g)
	mean := boxMean(s, 15)
	var xs, ys []float64
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.w; x++ {
			if s.at(x, y) < mean.at(x, y)-12 {
				xs = append(xs, float64(x)-float64(s.w)/2)
				ys = append(ys, float64(y)-float64(s.h)/2)
			}
		}
	}
	if len(xs) < 200 {
		return 0
	}
	diag := int(math.Hypot(float64(s.w), float64(s.h))) + 2
	bins := make([]float64, 2*diag)
	bestScore, bestAngle := -1.0, 0.0
	for a := -20.0; a <= 20.0; a += 0.25 {
		t := a * math.Pi / 180
		sin, cos := math.Sin(t), math.Cos(t)
		for i := range bins {
			bins[i] = 0
		}
		for i := range xs {
			row := int(xs[i]*sin+ys[i]*cos) + diag
			bins[row]++
		}
		score := 0.0
		for _, c := range bins {
			score += c * c
		}
		if score > bestScore {
			bestScore, bestAngle = score, a
		}
	}
	return bestAngle
}

// rotate turns the image by deg (counter-clockwise positive) around its
// centre, growing the canvas to fit, with bilinear sampling.
func rotate(g *grayImg, deg float64, fill float32) *grayImg {
	if deg == 0 {
		return g
	}
	t := deg * math.Pi / 180
	sin, cos := math.Sin(t), math.Cos(t)
	w := int(math.Ceil(math.Abs(float64(g.w)*cos) + math.Abs(float64(g.h)*sin)))
	h := int(math.Ceil(math.Abs(float64(g.w)*sin) + math.Abs(float64(g.h)*cos)))
	out := &grayImg{w: w, h: h, px: make([]float32, w*h)}
	cx, cy := float64(g.w)/2, float64(g.h)/2
	ox, oy := float64(w)/2, float64(h)/2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Inverse map: where in the source does this pixel come from?
			dx, dy := float64(x)-ox, float64(y)-oy
			sx := dx*cos - dy*sin + cx
			sy := dx*sin + dy*cos + cy
			out.px[y*w+x] = bilinear(g, sx, sy, fill)
		}
	}
	return out
}

func bilinear(g *grayImg, x, y float64, fill float32) float32 {
	if x < 0 || y < 0 || x > float64(g.w-1) || y > float64(g.h-1) {
		return fill
	}
	x0, y0 := int(x), int(y)
	x1, y1 := clampInt(x0+1, 0, g.w-1), clampInt(y0+1, 0, g.h-1)
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	top := g.at(x0, y0)*(1-fx) + g.at(x1, y0)*fx
	bot := g.at(x0, y1)*(1-fx) + g.at(x1, y1)*fx
	return top*(1-fy) + bot*fy
}

// percentile returns the p-th percentile (0–100) brightness.
func percentile(g *grayImg, p float64) float32 {
	var hist [256]int
	for _, v := range g.px {
		hist[clampInt(int(v), 0, 255)]++
	}
	target := int(float64(len(g.px)) * p / 100)
	n := 0
	for i, c := range hist {
		n += c
		if n > target {
			return float32(i)
		}
	}
	return 255
}

// pageMask marks the paper in a photo of a page lying on something darker.
// Blurred hard, the page is one bright patch (its text averages away) and
// the background a darker one; Otsu's method finds the level between them.
// It returns nil when there is no clear page-on-background split, as with a
// scan, so nothing is blanked.
func pageMask(g *grayImg) []bool {
	blur := boxMean(g, 25)
	var hist [256]float64
	for _, v := range blur.px {
		hist[clampInt(int(v), 0, 255)]++
	}
	total := float64(len(blur.px))
	var sumAll float64
	for i, c := range hist {
		sumAll += float64(i) * c
	}
	var wB, sumB, bestVar float64
	thresh := -1
	for t := 0; t < 256; t++ {
		wB += hist[t]
		if wB == 0 || wB == total {
			continue
		}
		sumB += float64(t) * hist[t]
		mB, mF := sumB/wB, (sumAll-sumB)/(total-wB)
		if v := wB * (total - wB) * (mB - mF) * (mB - mF); v > bestVar {
			bestVar, thresh = v, t
		}
	}
	if thresh < 0 {
		return nil
	}
	var nDark, nBright, sumDark, sumBright float64
	for i := 0; i <= thresh; i++ {
		nDark += hist[i]
		sumDark += float64(i) * hist[i]
	}
	nBright, sumBright = total-nDark, sumAll-sumDark
	if nDark < total*0.05 || nBright < total*0.2 || sumBright/nBright-sumDark/nDark < 40 {
		return nil
	}
	mask := make([]bool, len(blur.px))
	for i, v := range blur.px {
		mask[i] = int(v) > thresh
	}
	return mask
}

// cleanPhotoForOCR writes a cleaned-up copy of the image at path to a temp
// PNG and returns its path. The caller removes the file.
func cleanPhotoForOCR(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", err
	}
	g := median3(toGray(img))
	page := pageMask(g)

	// Even out the light: paper brightness = max filter (removes text) then
	// a wide blur; dividing by it makes the page white everywhere.
	bg := boxMean(maxFilter(boxMean(g, 1), 7), 30)
	for i, v := range g.px {
		d := bg.px[i]
		if d < 1 {
			d = 1
		}
		g.px[i] = float32(math.Min(255, float64(v/d*255)))
	}
	// Stretch contrast: the paper's typical brightness (the median, since
	// paper covers most of a page) becomes white, so grain on the paper
	// disappears and only the ink stays dark.
	lo, hi := percentile(g, 1), percentile(g, 50)
	if hi-lo > 10 {
		for i, v := range g.px {
			g.px[i] = float32(math.Max(0, math.Min(255, float64((v-lo)*255/(hi-lo)))))
		}
	}
	// Blank out the table or background around the page: once the light is
	// evened out its grain reads as pages of nonsense words.
	if page != nil {
		for i, inPage := range page {
			if !inPage {
				g.px[i] = 255
			}
		}
	}
	// Straighten; fill the new corners with white so they read as paper.
	g = rotate(g, -estimateSkew(g), 255)

	out := image.NewGray(image.Rect(0, 0, g.w, g.h))
	for i, v := range g.px {
		out.Pix[i] = uint8(clampInt(int(v+0.5), 0, 255))
	}
	tmp, err := os.CreateTemp("", "terminus-clean-*.png")
	if err != nil {
		return "", err
	}
	if err := png.Encode(tmp, out); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	return tmp.Name(), nil
}

// billWords counts the words and values a bill reading should contain, to
// compare two OCR readings of the same picture.
var billWords = regexp.MustCompile(`(?i)\b(?:invoice|receipt|date|bill|total|due|subtotal|tax|payment|statement|customer|account|amount|balance)\b|\$\s?\d+\.\d{2}\b|\b\d{4}-\d{2}-\d{2}\b|\b\d{1,2}/\d{1,2}/\d{2,4}\b`)

func ocrReadingScore(text string) int {
	return len(billWords.FindAllString(text, -1)) + len(moneyAmount.FindAllString(text, -1))
}

// billReadingScore judges a reading by what Terminus would actually get
// out of it: each bill field found is worth far more than loose words, so
// a cleaner-looking reading that loses the vendor or date does not win.
func billReadingScore(text string) int {
	repaired, _ := docpipeline.RepairOCRText(text)
	pr, _ := docpipeline.Run(repaired)
	f := pr.ToFieldMap(false)
	score := ocrReadingScore(text)
	for _, k := range []string{"vendor", "customer", "invoice_number", "date", "due_date", "total"} {
		if strings.TrimSpace(f[k]) != "" {
			score += 10
		}
	}
	return score
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
