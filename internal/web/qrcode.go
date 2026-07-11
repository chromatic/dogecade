package web

import (
	"encoding/base64"
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// qrDataURI renders data as a PNG QR code and returns it as a data: URI
// suitable for an <img src="..."> attribute, so pages don't need a separate
// QR image endpoint.
func qrDataURI(data string, sizePx int) (string, error) {
	return qrDataURIWithRecovery(data, qrcode.Medium, sizePx)
}

// qrDataURIWithRecovery is qrDataURI with an explicit error-correction
// level. Codes meant to be printed and stuck on physical hardware (versus
// shown transiently on a phone screen) should use a higher level: a
// scuffed, grease-smudged sticker on an arcade cabinet needs to keep
// scanning even with part of the code damaged.
func qrDataURIWithRecovery(data string, level qrcode.RecoveryLevel, sizePx int) (string, error) {
	png, err := qrcode.Encode(data, level, sizePx)
	if err != nil {
		return "", fmt.Errorf("failed to render QR code: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}
