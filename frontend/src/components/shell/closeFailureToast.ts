import type { CloseTabResult } from '~/generated/leapmux/v1/common_pb'
import { showWarnToast } from '~/components/common/Toast'

// toastCloseFailure surfaces a partial tab-close failure. No-op on
// success (empty failureMessage or missing result). The backend always
// pairs failureMessage with a failureDetail (err.Error()), but we guard
// against empty detail defensively.
export function toastCloseFailure(result: CloseTabResult | undefined): void {
  if (!result || !result.failureMessage)
    return
  showWarnToast(result.failureDetail ? `${result.failureMessage}: ${result.failureDetail}` : result.failureMessage)
}
