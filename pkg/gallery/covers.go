package gallery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func ContactSheetHash(galleryID int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("gallery-contact-sheet-%d", galleryID)))
	return hex.EncodeToString(sum[:])
}
