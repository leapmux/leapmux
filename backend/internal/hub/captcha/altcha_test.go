package captcha

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestManagerAltchaAlgorithmsVerifySolutions(t *testing.T) {
	tests := []struct {
		algorithm   string
		cost        int64
		memoryCost  int64
		parallelism int64
		deriveKey   altcha.DeriveKeyFunc
	}{
		{"SHA-256", 1000, 0, 0, altcha.DeriveKeySHA()},
		{"SHA-384", 1000, 0, 0, altcha.DeriveKeySHA()},
		{"SHA-512", 1000, 0, 0, altcha.DeriveKeySHA()},
		{"PBKDF2/SHA-256", 10000, 0, 0, altcha.DeriveKeyPBKDF2()},
		{"PBKDF2/SHA-384", 10000, 0, 0, altcha.DeriveKeyPBKDF2()},
		{"PBKDF2/SHA-512", 10000, 0, 0, altcha.DeriveKeyPBKDF2()},
		{"SCRYPT", 1024, 1, 1, altcha.DeriveKeyScrypt()},
		{"ARGON2ID", 1, 8192, 1, altcha.DeriveKeyArgon2id()},
	}
	for _, tt := range tests {
		t.Run(tt.algorithm, func(t *testing.T) {
			e := newTestManager(t, false)
			tuning := DefaultAltchaSettings()
			tuning.Algorithm, tuning.Cost = tt.algorithm, tt.cost
			tuning.MemoryCost, tuning.Parallelism = tt.memoryCost, tt.parallelism
			encodedTuning, err := json.Marshal(tuning)
			require.NoError(t, err)
			applyTestAltchaSettings(t, e, string(encodedTuning))

			challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
			require.NoError(t, err)
			var challenge altcha.Challenge
			require.NoError(t, json.Unmarshal([]byte(challengeJSON), &challenge))
			assert.Equal(t, tt.algorithm, challenge.Parameters.Algorithm)
			solution, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{
				Challenge: challenge,
				DeriveKey: tt.deriveKey,
			})
			require.NoError(t, err)
			require.NotNil(t, solution)

			payload := altcha.Payload{Challenge: challenge, Solution: *solution}
			payload.Solution.DerivedKey = "invalid"
			invalid, err := json.Marshal(payload)
			require.NoError(t, err)
			require.ErrorIs(t, e.m.Verify(context.Background(), "login", base64.StdEncoding.EncodeToString(invalid)), ErrVerificationFailed)

			// A refused solution must not consume the challenge's salt.
			payload.Solution = *solution
			valid, err := json.Marshal(payload)
			require.NoError(t, err)
			require.NoError(t, e.m.Verify(context.Background(), "login", base64.RawStdEncoding.EncodeToString(valid)))
			assert.ErrorIs(t, e.m.Verify(context.Background(), "login", base64.StdEncoding.EncodeToString(valid)), ErrVerificationFailed)
		})
	}
}

func TestManagerAltchaConcurrentVerificationConsumesSaltOnce(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	e := newTestManager(t, false)
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	challenge, err := e.m.AltchaChallengeJSON(t.Context())
	require.NoError(t, err)
	payload := solveChallenge(t, challenge)
	const callers = 8
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			results <- e.m.Verify(ctx, "login", payload)
		}()
	}
	close(start)
	accepted := 0
	for range callers {
		var err error
		select {
		case err = <-results:
		case <-ctx.Done():
			t.Fatal("concurrent CAPTCHA verification did not finish")
		}
		if err == nil {
			accepted++
		} else {
			assert.ErrorIs(t, err, ErrVerificationFailed)
		}
	}
	assert.Equal(t, 1, accepted)
}

type failingAltchaSaltStore struct {
	store.AltchaSaltsStore
	lookupError  error
	consumeError error
}

func (s failingAltchaSaltStore) HasAltchaSalt(ctx context.Context, salt string) (bool, error) {
	if s.lookupError != nil {
		return false, s.lookupError
	}
	return s.AltchaSaltsStore.HasAltchaSalt(ctx, salt)
}

func (s failingAltchaSaltStore) ConsumeAltchaSalt(ctx context.Context, params store.ConsumeAltchaSaltParams) (int64, error) {
	if s.consumeError != nil {
		return 0, s.consumeError
	}
	return s.AltchaSaltsStore.ConsumeAltchaSalt(ctx, params)
}

type altchaSaltStoreOverride struct {
	store.Store
	salts store.AltchaSaltsStore
}

func (s altchaSaltStoreOverride) AltchaSalts() store.AltchaSaltsStore { return s.salts }

func TestManagerAltchaSaltStoreFailures(t *testing.T) {
	failure := errors.New("salt ledger unavailable")
	for _, lookupFails := range []bool{true, false} {
		label := "consume failure"
		if lookupFails {
			label = "lookup failure"
		}
		t.Run(label, func(t *testing.T) {
			inner := newTestManager(t, false)
			salts := failingAltchaSaltStore{AltchaSaltsStore: inner.st.AltchaSalts()}
			if lookupFails {
				salts.lookupError = failure
			} else {
				salts.consumeError = failure
			}
			e := newTestManagerOver(t, altchaSaltStoreOverride{Store: inner.st, salts: salts}, inner.ks, testPublicURL, false)
			applyTestAltchaSettings(t, e, cheapAltchaSettings)
			challenge, err := e.m.AltchaChallengeJSON(t.Context())
			require.NoError(t, err)
			payload := solveChallenge(t, challenge)
			err = e.m.Verify(t.Context(), "login", payload)
			if lookupFails {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrVerificationFailed)
			}
			assert.ErrorIs(t, e.m.Verify(t.Context(), "login", payload), ErrVerificationFailed)
		})
	}
}
