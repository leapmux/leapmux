/**
 * Qwen Code e2e test fixtures.
 */
import { missingBinaryReason } from './helpers/binaryOnPath'
import { createProviderTest } from './provider-fixture-factory'
import { nativeContext, QWEN_AGENT } from './qwen-code/scenarios'

export const QWEN_E2E_SKIP_REASON: string | null = missingBinaryReason('qwen', 'Qwen Code E2E requires a qwen CLI on PATH')

export const qwenTest = createProviderTest({
  agent: QWEN_AGENT,
  nativeContext,
  skipReason: QWEN_E2E_SKIP_REASON,
  workspaceName: 'authenticatedQwenWorkspace',
})
