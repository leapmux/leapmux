package providerkit

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ClassifyHTTPDeliveryError distinguishes a native refusal from an unconfirmed delivery.
// A transport failure can occur after the server accepts the request.
func ClassifyHTTPDeliveryError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%w: provider did not confirm delivery for %s: %w", agent.ErrDeliveryUncertain, operation, err)
}
