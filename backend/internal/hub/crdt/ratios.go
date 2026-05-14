package crdt

// EqualRatios returns an n-element slice whose values each equal 1/n,
// matching the projection-invariant the validator enforces for SPLIT
// ratios / GRID row+col ratios. Returns nil when n <= 0 so callers can
// hand it straight to a SetNodeRatios op without checking length.
func EqualRatios(n int) []float64 {
	if n <= 0 {
		return nil
	}
	r := 1.0 / float64(n)
	out := make([]float64, n)
	for i := range out {
		out[i] = r
	}
	return out
}
