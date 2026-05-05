// Package captcha emits stateless math challenges (e.g. "7 + 3 = ?") with an
// HMAC-signed token that the form posts back. No DB / no session storage.
package captcha

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const validity = 5 * time.Minute

type Challenge struct {
	Question string // human-readable, "7 + 3 = ?"
	Token    string // pass back via hidden field; "answer:expiresUnix:hex(sig)"
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
	opPick, err := randInt(0, 2)
	if err != nil {
		return Challenge{}, err
	}

	var op string
	var answer int
	switch opPick {
	case 0:
		op, answer = "+", a+b
	case 1:
		// keep result non-negative
		if a < b {
			a, b = b, a
		}
		op, answer = "−", a-b
	default:
		op, answer = "×", a*b
	}

	expires := time.Now().Add(validity).Unix()
	payload := fmt.Sprintf("%d:%d", answer, expires)
	sig := sign(secret, payload)

	return Challenge{
		Question: fmt.Sprintf("%d %s %d = ?", a, op, b),
		Token:    payload + ":" + sig,
	}, nil
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
