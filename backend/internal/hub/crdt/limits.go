package crdt

const (
	// MaxGridDimension caps grid rows and columns. Mirrors the
	// frontend constant so layout ops that exceed the cap are
	// rejected uniformly on both sides.
	MaxGridDimension uint32 = 20
)
