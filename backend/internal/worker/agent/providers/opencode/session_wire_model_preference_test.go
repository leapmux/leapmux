package opencode

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeModelVariantPreferenceReadsNativeHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		records string
		variant string
		found   bool
		invalid bool
	}{
		{"empty history", `[]`, "", false, false},
		{"latest same model", `[{"id":"s1","type":"model-switched","model":{"providerID":"p","id":"a","variant":"high"},"previous":{"providerID":"p","id":"a","variant":"low"}}]`, "high", true, false},
		{"explicit default", `[{"id":"s1","type":"model-switched","model":{"providerID":"p","id":"a"},"previous":{"providerID":"p","id":"a","variant":"low"}}]`, "", true, false},
		{"departure", `[{"id":"s1","type":"model-switched","model":{"providerID":"p","id":"b"},"previous":{"providerID":"p","id":"a","variant":"low"}}]`, "low", true, false},
		{"other provider", `[{"id":"s1","type":"model-switched","model":{"providerID":"other","id":"a","variant":"high"}}]`, "", false, false},
		{"missing selected model", `[{"id":"s1","type":"model-switched"}]`, "", false, true},
		{"incomplete reference", `[{"id":"s1","type":"model-switched","model":{"providerID":"p"}}]`, "", false, true},
		{"wrong type", `[{"id":"s1","type":"assistant"}]`, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "model-switched", r.URL.Query().Get("type"))
				assert.Equal(t, "desc", r.URL.Query().Get("order"))
				_, _ = fmt.Fprintf(w, `{"data":%s,"cursor":{}}`, tc.records)
			})
			variant, found, err := rpc.modelVariantPreference(t.Context(), "ses_1", openCodeModelRef{ProviderID: "p", ID: "a"})
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.variant, variant)
			assert.Equal(t, tc.found, found)
		})
	}
}

func TestOpenCodeModelVariantPreferenceRetainsFilterAcrossPages(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "model-switched", r.URL.Query().Get("type"))
		if reads.Add(1) == 1 {
			assert.Equal(t, "desc", r.URL.Query().Get("order"))
			_, _ = w.Write([]byte(`{"data":[{"id":"s2","type":"model-switched","model":{"providerID":"p","id":"b"}}],"cursor":{"next":"older"}}`))
			return
		}
		assert.Empty(t, r.URL.Query().Get("order"))
		assert.Equal(t, "older", r.URL.Query().Get("cursor"))
		_, _ = w.Write([]byte(`{"data":[{"id":"s1","type":"model-switched","model":{"providerID":"p","id":"a","variant":"low"}}],"cursor":{"next":"not-needed"}}`))
	})
	variant, found, err := rpc.modelVariantPreference(t.Context(), "ses_1", openCodeModelRef{ProviderID: "p", ID: "a"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "low", variant)
	assert.EqualValues(t, 2, reads.Load())
}
