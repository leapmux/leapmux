package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// InitializeAccountState reads user presence before the Hub serves requests.
// Successful setup publishes presence only after its creation transaction commits.
func (s *AuthService) InitializeAccountState(ctx context.Context) error {
	present, err := s.checkHasAnyUser(ctx)
	if err != nil {
		return fmt.Errorf("initialize account state: %w", err)
	}
	if present {
		if _, err := s.store.Users().GetFirstAdmin(ctx); errors.Is(err, store.ErrNotFound) {
			return errors.New("the Hub has users but no administrator; stop the Hub and run leapmux recover bootstrap create-admin")
		} else if err != nil {
			return fmt.Errorf("check the initial administrator: %w", err)
		}
	}
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
		if s.hasUsers.Load() {
			return nil, "", errSetupAlreadyComplete
		}
		if !s.setupClaim.CompareAndSwap(false, true) {
			return nil, "", errSetupInProgress
		}
		defer s.setupClaim.Store(false)
		err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
			// This rare check also coordinates with direct bootstrap writers
			// and a predecessor whose account transaction remains in flight.
			present, err := tx.Users().LockAccountLifecycle(ctx)
			if err != nil {
				return err
			}
			if present {
				return errSetupAlreadyComplete
			}
			user, code, err = createUserInTx(ctx, tx, opt)
			return err
		})
		if err != nil {
			user, code = nil, ""
		}
	case signupOrdinary:
		if opt.isAdmin {
			return nil, "", errors.New("ordinary signup cannot grant administrator access")
		}
		present, readErr := s.checkHasAnyUser(ctx)
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
	if err == nil || errors.Is(err, errSetupAlreadyComplete) {
		// Publish only committed presence. A failed initial creation leaves
		// other callers in setup state and releases the separate claim.
		s.hasUsers.Store(true)
	}
	return user, code, err
}
