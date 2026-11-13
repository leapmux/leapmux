package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/hub/store"
)

const (
	accountStateUninitialized uint32 = iota
	accountStateEmpty
	accountStatePopulated
	accountStateSetupUncertain
)

// InitializeAccountState reads account state once before the Hub serves requests.
// Later setup publishes presence only after its creation transaction commits.
func (s *AuthService) InitializeAccountState(ctx context.Context) error {
	if s.accountState.Load() == accountStateSetupUncertain {
		return errSetupCommitUncertain
	}
	if s.accountState.Load() != accountStateUninitialized {
		return nil
	}
	present, err := s.store.Users().HasAny(ctx)
	if err != nil {
		return fmt.Errorf("initialize account state: %w", err)
	}
	state := accountStateEmpty
	if present {
		if _, err := s.store.Users().GetFirstAdmin(ctx); errors.Is(err, store.ErrNotFound) {
			return errors.New("the Hub has users but no administrator; stop the Hub and run leapmux recover bootstrap create-admin")
		} else if err != nil {
			return fmt.Errorf("check the initial administrator: %w", err)
		}
		state = accountStatePopulated
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Another initialization or completed setup must never be overwritten.
	s.accountState.CompareAndSwap(accountStateUninitialized, state)
	return nil
}

// signupAdmission distinguishes initial setup from ordinary public signup.
// Zero is invalid, so a new signup caller must choose its authority explicitly.
type signupAdmission uint8

const (
	signupInitialAdministrator signupAdmission = iota + 1
	signupOrdinary
)

var (
	errSetupCommitUncertain       = errors.New("setup commit outcome is unknown; restart the Hub before retrying setup")
	errAccountStateUninitialized  = errors.New("account state must be initialized before serving requests")
	errSetupAlreadyComplete       = errors.New("setup is complete; sign in before creating another account")
	errSetupInProgress            = errors.New("setup is in progress; try again after it finishes")
	errSignupRequiresExistingUser = errors.New("complete initial setup before signing up")
)

// createSignupUser owns signup admission. The lower-level creation helper only
// writes atomic account state and cannot admit a public signup on its own.
func (s *AuthService) createSignupUser(ctx context.Context, admission signupAdmission, opt createUserTxParams) (*store.User, string, error) {
	var user *store.User
	var code string
	var err error
	switch admission {
	case signupInitialAdministrator:
		if !opt.isAdmin {
			return nil, "", errors.New("initial setup must create an administrator")
		}
		if !s.setupClaim.CompareAndSwap(false, true) {
			return nil, "", errSetupInProgress
		}
		defer s.setupClaim.Store(false)
		// Check after acquiring the claim: a stale request can arrive after
		// another setup committed and released its claim.
		present, readErr := s.checkHasAnyUser()
		if readErr != nil {
			return nil, "", readErr
		}
		if present {
			return nil, "", errSetupAlreadyComplete
		}
		user, code, err = createUserInTx(ctx, s.store, opt)
		if store.CommitOutcomeUnknown(err) {
			// An absent acknowledgement does not prove rollback. Keep setup
			// closed until a fresh Hub initializes from the database again.
			s.accountState.Store(accountStateSetupUncertain)
			slog.ErrorContext(ctx, "initial account commit outcome is unknown", "err", err)
			return nil, "", errSetupCommitUncertain
		}
	case signupOrdinary:
		if opt.isAdmin {
			return nil, "", errors.New("ordinary signup cannot grant administrator access")
		}
		present, readErr := s.checkHasAnyUser()
		if readErr != nil {
			return nil, "", readErr
		}
		if !present {
			return nil, "", errSignupRequiresExistingUser
		}
		user, code, err = createUserInTx(ctx, s.store, opt)
	default:
		return nil, "", errors.New("signup requires an explicit admission policy")
	}
	if err == nil {
		// Publish only committed presence. A confirmed rollback leaves setup
		// open and releases the separate claim.
		s.accountState.Store(accountStatePopulated)
	}
	return user, code, err
}
