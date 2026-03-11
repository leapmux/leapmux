package noise

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// wordlist contains 256 curated, distinct, easy-to-read English words distributed
// across A-Z. Each byte of the BLAKE2b hash maps to one word, producing a 4-word
// fingerprint. This list must be identical in both Go and TypeScript implementations.
var wordlist = [256]string{
	// A (12)
	"able", "acid", "aged", "aqua", "arch", "area", "army", "atom",
	"auto", "avid", "away", "axis",
	// B (11)
	"back", "ball", "band", "bark", "barn", "base", "bell", "bird",
	"blow", "blue", "bold",
	// C (11)
	"cage", "calm", "cape", "card", "cave", "chip", "city", "clay",
	"coal", "code", "crew",
	// D (11)
	"dale", "damp", "dark", "dawn", "deal", "deep", "desk", "dial",
	"dish", "dock", "dome",
	// E (11)
	"each", "earn", "ease", "east", "echo", "edge", "epic", "euro",
	"even", "evil", "exam",
	// F (11)
	"face", "fact", "fair", "fame", "farm", "fear", "file", "firm",
	"fish", "folk", "fuse",
	// G (11)
	"gale", "game", "gate", "gave", "gear", "gift", "glad", "glow",
	"goat", "gold", "grid",
	// H (10)
	"hair", "half", "hall", "halt", "hand", "hare", "hawk", "head",
	"hear", "helm",
	// I (10)
	"icon", "idea", "idle", "inch", "info", "into", "iron", "iris",
	"isle", "item",
	// J (8)
	"jack", "jade", "jail", "jazz", "jest", "jilt", "joke", "jolt",
	// K (8)
	"kale", "keen", "kelp", "kept", "kiln", "kind", "king", "kite",
	// L (12)
	"lace", "lake", "lamb", "lamp", "lane", "lark", "late", "lawn",
	"lead", "leaf", "lens", "lime",
	// M (11)
	"mace", "malt", "mare", "mask", "mate", "maze", "mild", "mint",
	"mist", "moat", "moon",
	// N (11)
	"nail", "name", "navy", "neat", "neck", "nest", "news", "nine",
	"node", "norm", "nova",
	// O (10)
	"oath", "obey", "odds", "omen", "once", "only", "opal", "open",
	"oven", "oxen",
	// P (12)
	"pace", "palm", "pane", "park", "path", "peak", "pine", "plum",
	"poem", "pond", "port", "prey",
	// Q (4)
	"quad", "quay", "quip", "quiz",
	// R (12)
	"race", "raft", "raid", "rain", "rake", "rank", "rare", "reed",
	"rein", "roam", "robe", "ruby",
	// S (13)
	"safe", "sage", "sail", "salt", "sand", "seal", "shed", "silk",
	"snap", "soar", "star", "stem", "swan",
	// T (13)
	"tack", "tale", "tame", "tank", "tape", "task", "teal", "tide",
	"toad", "toll", "tone", "trek", "turf",
	// U (8)
	"ugly", "undo", "unit", "unto", "upon", "urge", "used", "user",
	// V (8)
	"vain", "vale", "vane", "vast", "veil", "vent", "view", "vine",
	// W (13)
	"wade", "wage", "wake", "walk", "wall", "wane", "ward", "warm",
	"warn", "wave", "weld", "wilt", "wren",
	// Y (8)
	"yard", "yarn", "yawn", "year", "yell", "yoga", "yoke", "yore",
	// Z (7)
	"zany", "zeal", "zero", "zest", "zinc", "zone", "zoom",
}

// CompositeKeyFingerprint generates a 4-word fingerprint from the concatenation
// of x25519, ML-KEM, and SLH-DSA public key bytes: BLAKE2b(x25519 || mlkem || slhdsa).
func CompositeKeyFingerprint(x25519Pub, mlkemPub, slhdsaPub []byte) string {
	composite := make([]byte, 0, len(x25519Pub)+len(mlkemPub)+len(slhdsaPub))
	composite = append(composite, x25519Pub...)
	composite = append(composite, mlkemPub...)
	composite = append(composite, slhdsaPub...)
	return KeyFingerprint(composite)
}

// KeyFingerprint generates a human-friendly 4-word fingerprint from a
// public key. The fingerprint is derived by hashing the key with BLAKE2b
// and mapping the first 4 bytes to the wordlist.
func KeyFingerprint(publicKey []byte) string {
	h := blake2b.Sum256(publicKey)
	words := make([]string, 4)
	for i := 0; i < 4; i++ {
		words[i] = wordlist[h[i]]
	}
	return strings.Join(words, "-")
}

// KeyFingerprintHex generates a fingerprint from a hex-encoded public key
// (or concatenated composite key bytes).
func KeyFingerprintHex(publicKeyHex string) (string, error) {
	if len(publicKeyHex)%2 != 0 || len(publicKeyHex) == 0 {
		return "", fmt.Errorf("invalid public key hex length: %d", len(publicKeyHex))
	}
	key := make([]byte, len(publicKeyHex)/2)
	for i := range key {
		b, err := hexByte(publicKeyHex[i*2], publicKeyHex[i*2+1])
		if err != nil {
			return "", err
		}
		key[i] = b
	}
	return KeyFingerprint(key), nil
}

func hexByte(hi, lo byte) (byte, error) {
	h, err := hexNibble(hi)
	if err != nil {
		return 0, err
	}
	l, err := hexNibble(lo)
	if err != nil {
		return 0, err
	}
	return h<<4 | l, nil
}

func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	default:
		return 0, fmt.Errorf("invalid hex character: %c", c)
	}
}
