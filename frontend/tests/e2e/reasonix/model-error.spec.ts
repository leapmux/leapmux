import { randomUUID } from 'node:crypto'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseModelError } from '../helpers/nativeModelError'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('shows the native model failure and accepts a later valid prompt', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  // Reasonix 1.38.7 masks each credential-shaped token of a provider error as
  // `****` before the error reaches its client. Its patterns include 32 or more
  // hexadecimal digits and 40 or more base64 characters in a row. The usual
  // marker (`NATIVEERROR` and 32 letters, see `nativeErrorMarker`) is one run of
  // 43 characters, so it arrives as `****`. Each word of this marker has at most
  // 12 characters, so the error keeps it.
  const message = `NATIVEERROR ${randomUUID().replaceAll('-', ' ')}`
  await exerciseModelError(context, { queueAfterFailure: 'running', error: { status: 400, code: 'invalid_request_error', message } })
})
