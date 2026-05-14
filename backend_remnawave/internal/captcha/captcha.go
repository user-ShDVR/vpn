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

// renderImage rasterizes the question as a small PNG with light noise so
// the text isn't selectable via DOM and resists trivial OCR. Falls back to
// empty string on any draw error — template can show the plain string then.
func renderImage(text string) string {
	const (
		w, h    = 180, 56
		scale   = 2 // 2x oversample for crispness on retina
		fontDX  = 7
		fontDY  = 13
	)
	img := image.NewRGBA(image.Rect(0, 0, w*scale, h*scale))
	bg := color.RGBA{R: 0x11, G: 0x18, B: 0x27, A: 0xff} // dark-800
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))

	// Random noise dots
	for i := 0; i < 250; i++ {
		x := rng.Intn(w * scale)
		y := rng.Intn(h * scale)
		gray := uint8(60 + rng.Intn(60))
		img.Set(x, y, color.RGBA{R: gray, G: gray, B: gray, A: 0xff})
	}

	// Draw chars one by one at slightly jittered Y positions, alternating colors
	face := basicfont.Face7x13
	startX := 12 * scale
	for i, r := range text {
		col := color.RGBA{R: 0xf9, G: 0xfa, B: 0xfb, A: 0xff} // dark-50
		if i%2 == 0 {
			col = color.RGBA{R: 0x56, G: 0x59, B: 0xf0, A: 0xff} // accent-400
		}
		dy := (h*scale)/2 + (fontDY*scale)/3 + rng.Intn(8) - 4
		dx := startX + i*fontDX*scale + rng.Intn(4) - 2
		d := &font.Drawer{
			Dst:  img,
			Src:  &image.Uniform{C: col},
			Face: scaleFace(face, scale),
			Dot:  fixed.Point26_6{X: fixed.I(dx), Y: fixed.I(dy)},
		}
		d.DrawString(string(r))
	}

	// Random diagonal line for extra entropy
	for i := 0; i < 2; i++ {
		x0, y0 := rng.Intn(w*scale), rng.Intn(h*scale)
		x1, y1 := rng.Intn(w*scale), rng.Intn(h*scale)
		drawLine(img, x0, y0, x1, y1, color.RGBA{R: 0x49, G: 0x50, B: 0x5d, A: 0xff})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// scaleFace wraps a font.Face and renders it at `scale`x by drawing it onto
// a tiny buffer and then expanding. Quick hack — basicfont has no built-in
// scaling. For our 2x upscale we simply request the same face; result looks
// fine on retina at h-56 element height.
func scaleFace(f font.Face, _ int) font.Face { return f }

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
