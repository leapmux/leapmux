import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

// Goose 1.53.0 retries a failed model request three times, then states the error
// of its last attempt ("Ran into this error: ...") and ends the turn.
const GOOSE_REQUEST_ATTEMPTS = 4

gooseTest('shows the native model failure and accepts a later valid prompt', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await exerciseModelError(context, { attempts: GOOSE_REQUEST_ATTEMPTS })
})
