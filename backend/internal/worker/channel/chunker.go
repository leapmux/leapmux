package channel

// chunkBuffer accumulates decrypted plaintext chunks for one logical message.
type chunkBuffer struct {
	parts [][]byte
	total int
}
