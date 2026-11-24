package keystore

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCiphertextValueRejectsZeroAndTruncatedValues(t *testing.T) {
	var zero Ciphertext
	_, err := zero.Value()
	require.Error(t, err)

	short := Ciphertext{raw: make([]byte, overhead-1)}
	_, err = short.Value()
	require.Error(t, err)
}

func TestCiphertextScanRejectsNilAndEmptyAndClearsPriorState(t *testing.T) {
	valid := make([]byte, overhead)
	valid[3] = 1
	var ct Ciphertext
	require.NoError(t, ct.Scan(valid))
	require.NotEmpty(t, ct.Bytes())

	assert.Error(t, ct.Scan(nil))
	assert.Nil(t, ct.Bytes(), "failed Scan must clear prior Ciphertext bytes")

	require.NoError(t, ct.Scan(valid))
	assert.Error(t, ct.Scan([]byte{}))
	assert.Nil(t, ct.Bytes(), "failed empty Scan must clear prior Ciphertext bytes")

	require.NoError(t, ct.Scan(valid))
	assert.Error(t, ct.Scan("string"))
	assert.Nil(t, ct.Bytes(), "failed wrong-type Scan must clear prior Ciphertext bytes")
}

func TestCiphertextScanClonesDriverBuffer(t *testing.T) {
	driverBuf := make([]byte, overhead)
	driverBuf[3] = 1
	driverBuf[4] = 10
	driverBuf[5] = 20
	driverBuf[6] = 30
	original := bytes.Clone(driverBuf)

	var ct Ciphertext
	require.NoError(t, ct.Scan(driverBuf))

	// Mutate the driver buffer after Scan.
	driverBuf[4] = 99
	assert.Equal(t, original, ct.Bytes())

	// Also verify Bytes() returns a clone.
	got := ct.Bytes()
	got[5] = 88
	assert.Equal(t, original, ct.Bytes())

	// Value() exposes the stored bytes without an extra copy.
	val, err := ct.Value()
	require.NoError(t, err)
	valBytes, ok := val.([]byte)
	require.True(t, ok)
	assert.Equal(t, original, valBytes)
}

func TestNullCiphertextEdgeCases(t *testing.T) {
	t.Run("nil scan produces invalid NULL value", func(t *testing.T) {
		var nc NullCiphertext
		require.NoError(t, nc.Scan([]byte{1, 2, 3, 4}))
		require.True(t, nc.Valid())

		require.NoError(t, nc.Scan(nil))
		assert.False(t, nc.Valid())
		assert.Nil(t, nc.Bytes())

		val, err := nc.Value()
		require.NoError(t, err)
		assert.Nil(t, val)

		_, err = nc.Version()
		assert.Error(t, err)
	})

	t.Run("valid with empty or truncated raw fails Value", func(t *testing.T) {
		nc := NullCiphertext{valid: true}
		_, err := nc.Value()
		require.Error(t, err)

		short := NullCiphertext{raw: make([]byte, overhead-1), valid: true}
		_, err = short.Value()
		require.Error(t, err)
	})

	t.Run("scan rejects empty slice and wrong type and clears prior state", func(t *testing.T) {
		valid := make([]byte, overhead)
		valid[3] = 1
		var nc NullCiphertext
		require.NoError(t, nc.Scan(valid))
		require.True(t, nc.Valid())

		assert.Error(t, nc.Scan([]byte{}))
		assert.False(t, nc.Valid(), "failed empty Scan must clear prior NullCiphertext validity")
		assert.Nil(t, nc.Bytes(), "failed empty Scan must clear prior NullCiphertext bytes")

		require.NoError(t, nc.Scan(valid))
		assert.Error(t, nc.Scan("string"))
		assert.False(t, nc.Valid(), "failed wrong-type Scan must clear prior NullCiphertext validity")
		assert.Nil(t, nc.Bytes(), "failed wrong-type Scan must clear prior NullCiphertext bytes")
	})

	t.Run("scan valid bytes clones buffer and Value returns stored bytes", func(t *testing.T) {
		buf := make([]byte, overhead)
		buf[3] = 2
		buf[4] = 5
		buf[5] = 6
		original := bytes.Clone(buf)

		var nc NullCiphertext
		require.NoError(t, nc.Scan(buf))
		assert.True(t, nc.Valid())

		buf[4] = 99
		assert.Equal(t, original, nc.Bytes())

		gotBytes := nc.Bytes()
		gotBytes[5] = 88
		assert.Equal(t, original, nc.Bytes())

		val, err := nc.Value()
		require.NoError(t, err)
		valBytes, ok := val.([]byte)
		require.True(t, ok)
		assert.Equal(t, original, valBytes)

		ver, err := nc.Version()
		require.NoError(t, err)
		assert.Equal(t, uint32(2), ver)
	})
}

func TestFromRawCiphertextAndVersion(t *testing.T) {
	_, err := fromRawCiphertext(make([]byte, overhead-1))
	require.Error(t, err)

	var zero Ciphertext
	_, err = zero.Version()
	require.Error(t, err)

	short := Ciphertext{raw: []byte{1, 2}}
	_, err = short.Version()
	require.Error(t, err)

	ks, err := NewRandom()
	require.NoError(t, err)
	assert.NotEqual(t, [keySize]byte{}, ks.Pepper())

	raw, err := ks.Encrypt([]byte("hello"), ColumnAAD("t", "c", "k"))
	require.NoError(t, err)

	ct, err := fromRawCiphertext(raw)
	require.NoError(t, err)

	// Mutating raw after fromRawCiphertext must not affect ct.
	raw[0] = 0xff
	ver, err := ct.Version()
	require.NoError(t, err)
	assert.Equal(t, uint32(1), ver)
}

func TestSealOpenStringAndBytesEdgeCases(t *testing.T) {
	ks1, err := NewRandom()
	require.NoError(t, err)
	ks2, err := NewRandom()
	require.NoError(t, err)

	aad := ColumnAAD("test_table", "test_col", "row-1")
	wrongAAD := ColumnAAD("test_table", "test_col", "row-2")

	t.Run("SealString and OpenString roundtrip including empty", func(t *testing.T) {
		for _, plain := range []string{"", "hello", "unicode-\u2603-payload"} {
			ct, err := ks1.SealString(plain, aad)
			require.NoError(t, err)

			got, err := ks1.OpenString(ct, aad)
			require.NoError(t, err)
			assert.Equal(t, plain, got)

			_, err = ks1.OpenString(ct, wrongAAD)
			assert.Error(t, err)

			_, err = ks2.OpenString(ct, aad)
			assert.Error(t, err)
		}
	})

	t.Run("SealBytes and OpenBytes roundtrip including empty, non-UTF8, and 1 MiB", func(t *testing.T) {
		large := bytes.Repeat([]byte{0xab, 0xcd, 0x00, 0xff}, 256*1024)
		cases := [][]byte{
			{},
			{0x00, 0xff, 0xfe, 0x80},
			large,
		}
		for _, plain := range cases {
			ct, err := ks1.SealBytes(plain, aad)
			require.NoError(t, err)

			got, err := ks1.OpenBytes(ct, aad)
			require.NoError(t, err)
			assert.Equal(t, plain, got)
			assert.NotNil(t, got)
		}
	})

	t.Run("SealOptionalString and OpenOptionalString", func(t *testing.T) {
		ncNil, err := ks1.SealOptionalString(nil, aad)
		require.NoError(t, err)
		assert.False(t, ncNil.Valid())
		gotNil, err := ks1.OpenOptionalString(ncNil, aad)
		require.NoError(t, err)
		assert.Nil(t, gotNil)

		empty := ""
		ncEmpty, err := ks1.SealOptionalString(&empty, aad)
		require.NoError(t, err)
		assert.True(t, ncEmpty.Valid())
		gotEmpty, err := ks1.OpenOptionalString(ncEmpty, aad)
		require.NoError(t, err)
		require.NotNil(t, gotEmpty)
		assert.Equal(t, "", *gotEmpty)

		val := "val"
		ncVal, err := ks1.SealOptionalString(&val, aad)
		require.NoError(t, err)
		assert.True(t, ncVal.Valid())
		gotVal, err := ks1.OpenOptionalString(ncVal, aad)
		require.NoError(t, err)
		require.NotNil(t, gotVal)
		assert.Equal(t, "val", *gotVal)

		_, err = ks1.OpenOptionalString(ncVal, wrongAAD)
		assert.Error(t, err)
	})

	t.Run("SealOptionalBytes and OpenOptionalBytes", func(t *testing.T) {
		ncNil, err := ks1.SealOptionalBytes(nil, aad)
		require.NoError(t, err)
		assert.False(t, ncNil.Valid())
		gotNil, err := ks1.OpenOptionalBytes(ncNil, aad)
		require.NoError(t, err)
		assert.Nil(t, gotNil)

		ncEmpty, err := ks1.SealOptionalBytes([]byte{}, aad)
		require.NoError(t, err)
		assert.True(t, ncEmpty.Valid())
		gotEmpty, err := ks1.OpenOptionalBytes(ncEmpty, aad)
		require.NoError(t, err)
		require.NotNil(t, gotEmpty)
		assert.Empty(t, gotEmpty)

		raw := []byte{0x01, 0x02, 0xff}
		ncRaw, err := ks1.SealOptionalBytes(raw, aad)
		require.NoError(t, err)
		assert.True(t, ncRaw.Valid())
		gotRaw, err := ks1.OpenOptionalBytes(ncRaw, aad)
		require.NoError(t, err)
		assert.Equal(t, raw, gotRaw)
	})

	t.Run("SealNonEmptyString, SealNonEmptyBytes, and OpenStringOrDefault", func(t *testing.T) {
		ncEmptyStr, err := ks1.SealNonEmptyString("", aad)
		require.NoError(t, err)
		assert.False(t, ncEmptyStr.Valid())
		gotDefault, err := ks1.OpenStringOrDefault(ncEmptyStr, aad, "{}")
		require.NoError(t, err)
		assert.Equal(t, "{}", gotDefault)

		ncStr, err := ks1.SealNonEmptyString(`{"theme":"dark"}`, aad)
		require.NoError(t, err)
		assert.True(t, ncStr.Valid())
		gotStr, err := ks1.OpenStringOrDefault(ncStr, aad, "{}")
		require.NoError(t, err)
		assert.Equal(t, `{"theme":"dark"}`, gotStr)

		_, err = ks1.OpenStringOrDefault(ncStr, wrongAAD, "{}")
		assert.Error(t, err)

		ncNilBytes, err := ks1.SealNonEmptyBytes(nil, aad)
		require.NoError(t, err)
		assert.False(t, ncNilBytes.Valid())

		ncEmptyBytes, err := ks1.SealNonEmptyBytes([]byte{}, aad)
		require.NoError(t, err)
		assert.False(t, ncEmptyBytes.Valid())

		ncBytes, err := ks1.SealNonEmptyBytes([]byte{9, 8}, aad)
		require.NoError(t, err)
		assert.True(t, ncBytes.Valid())
		gotBytes, err := ks1.OpenOptionalBytes(ncBytes, aad)
		require.NoError(t, err)
		assert.Equal(t, []byte{9, 8}, gotBytes)
	})

	t.Run("unknown key version fails", func(t *testing.T) {
		k1, err := GenerateKey()
		require.NoError(t, err)
		k2, err := GenerateKey()
		require.NoError(t, err)
		ksV2, err := New(map[uint32][keySize]byte{2: k2})
		require.NoError(t, err)
		ksV1, err := New(map[uint32][keySize]byte{1: k1})
		require.NoError(t, err)

		ct, err := ksV2.SealString("secret", aad)
		require.NoError(t, err)

		_, err = ksV1.OpenString(ct, aad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown key version")
	})

	t.Run("nil keystore returns error on every seal and open helper", func(t *testing.T) {
		var nilKS *Keystore
		_, err := nilKS.SealString("x", aad)
		require.Error(t, err)
		_, err = nilKS.SealOptionalBytes(nil, aad)
		require.Error(t, err)
		_, err = nilKS.OpenOptionalBytes(NullCiphertext{}, aad)
		require.Error(t, err)
		_, err = nilKS.SealOptionalString(nil, aad)
		require.Error(t, err)
		_, err = nilKS.OpenOptionalString(NullCiphertext{}, aad)
		require.Error(t, err)
		_, err = nilKS.SealNonEmptyString("", aad)
		require.Error(t, err)
		_, err = nilKS.SealNonEmptyBytes(nil, aad)
		require.Error(t, err)
		_, err = nilKS.OpenStringOrDefault(NullCiphertext{}, aad, "{}")
		require.Error(t, err)

		ct, err := ks1.SealString("x", aad)
		require.NoError(t, err)
		_, err = nilKS.OpenString(ct, aad)
		require.Error(t, err)
	})
}

func TestColumnAADCollisionResistance(t *testing.T) {
	aads := [][]byte{
		ColumnAAD("t", "c", "a:b", "c"),
		ColumnAAD("t", "c", "a", "b:c"),
		ColumnAAD("t", "c", "a\x00b", "c"),
		ColumnAAD("t", "c", "a", "b\x00c"),
	}
	for i := range aads {
		for j := i + 1; j < len(aads); j++ {
			assert.NotEqual(t, aads[i], aads[j], "AAD %d and %d must not collide", i, j)
		}
	}

	// Empty string and NUL inside keyParts must succeed without panicking.
	emptyKey := ColumnAAD("t", "c", "")
	nulKey := ColumnAAD("t", "c", "\x00")
	assert.NotEmpty(t, emptyKey)
	assert.NotEmpty(t, nulKey)
	assert.NotEqual(t, emptyKey, nulKey)

	// Programmer errors must panic.
	assert.Panics(t, func() { ColumnAAD("", "c", "k") })
	assert.Panics(t, func() { ColumnAAD("t", "", "k") })
	assert.Panics(t, func() { ColumnAAD("t", "c") })
}
