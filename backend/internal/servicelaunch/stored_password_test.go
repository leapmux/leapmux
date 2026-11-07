package servicelaunch

import (
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeStoredPassword validates the UTF-16LE representation used by the
// Windows generic credential entry, without creating an immutable secret.
func decodeStoredPassword(blob []byte) ([]byte, error) {
	if len(blob) == 0 || len(blob) > 2560 || len(blob)%2 != 0 {
		return nil, fmt.Errorf("the stored credential must contain a UTF-16LE password")
	}
	encoded := make([]uint16, len(blob)/2)
	defer clear(encoded)
	for index := range encoded {
		encoded[index] = uint16(blob[index*2]) | uint16(blob[index*2+1])<<8
	}
	for index := 0; index < len(encoded); index++ {
		value := encoded[index]
		if value == 0 {
			return nil, fmt.Errorf("the stored password contains a null character")
		}
		if value >= 0xd800 && value <= 0xdbff {
			index++
			if index == len(encoded) || encoded[index] < 0xdc00 || encoded[index] > 0xdfff {
				return nil, fmt.Errorf("the stored password contains invalid UTF-16")
			}
		} else if value >= 0xdc00 && value <= 0xdfff {
			return nil, fmt.Errorf("the stored password contains invalid UTF-16")
		}
	}
	decoded := utf16.Decode(encoded)
	defer clear(decoded)
	var password []byte
	for _, value := range decoded {
		password = utf8.AppendRune(password, value)
	}
	return password, nil
}
