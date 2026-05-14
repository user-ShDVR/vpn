// Package captcha emits stateless math challenges (e.g. "7 + 3 = ?") with an
// HMAC-signed token that the form posts back. No DB / no session storage.
package captcha

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/big"
	mrand "math/rand"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const validity = 5 * time.Minute

type Challenge struct {
	Question    string // human-readable, "7 + 3 = ?" (kept for fallback / a11y)
	QuestionDataURL string // "data:image/png;base64,..." rendered captcha image
	Token       string // pass back via hidden field; "answer:expiresUnix:hex(sig)"
}

// New generates a fresh challenge signed with secret.
func New(secret string) (Challenge, error) {
	a, err := randInt(1, 9)
	if err != nil {
		return Challenge{}, err
	}
	b, err := randInt(1, 9)
	if err != nil {
		return Challenge{}, err
	}
	opPick, err := randInt(0, 1)
	if err != nil {
		return Challenge{}, err
	}

	var op string
	var answer int
	if opPick == 0 {
		op, answer = "+", a+b
	} else {
		// keep result non-negative
		if a < b {
			a, b = b, a
		}
		op, answer = "−", a-b
	}

	expires := time.Now().Add(validity).Unix()
	payload := fmt.Sprintf("%d:%d", answer, expires)
	sig := sign(secret, payload)

	question := fmt.Sprintf("%d %s %d = ?", a, op, b)
	return Challenge{
		Question:        question,
		QuestionDataURL: renderImage(question),
		Token:           payload + ":" + sig,
	}, nil
}

// renderImage rasterizes the question as a PNG. basicfont is fixed at 7x13,
// so we draw each glyph onto a tiny temp image and then nearest-neighbor
// upscale 4x onto the final canvas — gives ~52px tall glyphs at low overhead.
// Noise dots + diagonal lines drawn on top of the upscale so they stay 1px
// crisp (harder for OCR to filter out).
func renderImage(text string) string {
	const (
		scale  = 4  // each pixel of the 7x13 font becomes scale×scale block
		padX   = 6  // left/right padding in small-image px
		padY   = 4  // top/bottom padding in small-image px
		fontDX = 7
		fontDY = 13
	)
	n := len(text)
	smallW := padX*2 + n*fontDX
	smallH := padY*2 + fontDY + 4

	small := image.NewRGBA(image.Rect(0, 0, smallW, smallH))
	bg := color.RGBA{R: 0x11, G: 0x18, B: 0x27, A: 0xff} // dark-800
	draw.Draw(small, small.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	face := basicfont.Face7x13

	// Render each char with jittered Y + alternating colors.
	for i, r := range text {
		col := color.RGBA{R: 0xf9, G: 0xfa, B: 0xfb, A: 0xff} // dark-50
		if i%2 == 0 {
			col = color.RGBA{R: 0x86, G: 0x89, B: 0xf0, A: 0xff} // accent-400
		}
		dy := padY + fontDY - 1 + rng.Intn(3) - 1
		dx := padX + i*fontDX + rng.Intn(2) - 1
		d := &font.Drawer{
			Dst:  small,
			Src:  &image.Uniform{C: col},
			Face: face,
			Dot:  fixed.Point26_6{X: fixed.I(dx), Y: fixed.I(dy)},
		}
		d.DrawString(string(r))
	}

	// Nearest-neighbor upscale to the final canvas.
	bigW := smallW * scale
	bigH := smallH * scale
	big := image.NewRGBA(image.Rect(0, 0, bigW, bigH))
	for y := 0; y < smallH; y++ {
		for x := 0; x < smallW; x++ {
			c := small.RGBAAt(x, y)
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					big.SetRGBA(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}

	// Noise dots on the upscaled canvas (keep them 1px).
	for i := 0; i < 350; i++ {
		x := rng.Intn(bigW)
		y := rng.Intn(bigH)
		gray := uint8(60 + rng.Intn(60))
		big.Set(x, y, color.RGBA{R: gray, G: gray, B: gray, A: 0xff})
	}
	// A couple of obscuring diagonal lines.
	for i := 0; i < 2; i++ {
		x0, y0 := rng.Intn(bigW), rng.Intn(bigH)
		x1, y1 := rng.Intn(bigW), rng.Intn(bigH)
		drawLine(big, x0, y0, x1, y1, color.RGBA{R: 0x49, G: 0x50, B: 0x5d, A: 0xff})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// drawLine — Bresenham-ish, good enough for the captcha noise overlay.
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := 1, 1
	if x0 >= x1 {
		sx = -1
	}
	if y0 >= y1 {
		sy = -1
	}
	err := dx + dy
	for {
		img.Set(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

var ErrCaptchaInvalid = errors.New("captcha invalid")
var ErrCaptchaExpired = errors.New("captcha expired")
var ErrCaptchaWrong = errors.New("captcha answer wrong")

// Verify checks token against userAnswer.
func Verify(secret, token, userAnswer string) error {
	parts := strings.SplitN(token, ":", 3)
	if len(parts) != 3 {
		return ErrCaptchaInvalid
	}
	answerStr, expiresStr, gotSig := parts[0], parts[1], parts[2]

	wantSig := sign(secret, answerStr+":"+expiresStr)
	if !hmac.Equal([]byte(wantSig), []byte(gotSig)) {
		return ErrCaptchaInvalid
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return ErrCaptchaInvalid
	}
	if time.Now().Unix() > expires {
		return ErrCaptchaExpired
	}

	if strings.TrimSpace(userAnswer) != answerStr {
		return ErrCaptchaWrong
	}
	return nil
}

func sign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func randInt(minV, maxV int) (int, error) {
	delta := int64(maxV - minV + 1)
	n, err := rand.Int(rand.Reader, big.NewInt(delta))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()) + minV, nil
}
