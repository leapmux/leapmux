package keystore

import "encoding/binary"

// ColumnAAD builds canonical length-prefixed Additional Authenticated Data
// binding a ciphertext to its table, column, and row key parts.
//
// Each component is framed as [uint32 big-endian length][raw bytes], preventing
// ambiguity or collisions for arbitrary runtime key strings (including "" and
// strings containing NUL or delimiter bytes).
//
// ColumnAAD panics only on programmer errors: empty table, empty column, or
// zero keyParts. Runtime key values in keyParts may be empty.
func ColumnAAD(table, column string, keyParts ...string) []byte {
	if table == "" {
		panic("keystore: ColumnAAD requires non-empty table")
	}
	if column == "" {
		panic("keystore: ColumnAAD requires non-empty column")
	}
	if len(keyParts) == 0 {
		panic("keystore: ColumnAAD requires at least one key part")
	}

	totalLen := 4 + len(table) + 4 + len(column)
	for _, p := range keyParts {
		totalLen += 4 + len(p)
	}

	buf := make([]byte, 0, totalLen)
	buf = appendAADPart(buf, table)
	buf = appendAADPart(buf, column)
	for _, p := range keyParts {
		buf = appendAADPart(buf, p)
	}
	return buf
}

func appendAADPart(buf []byte, part string) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(part)))
	buf = append(buf, lenBuf[:]...)
	return append(buf, part...)
}
