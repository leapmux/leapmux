import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseGoosePlanLimit } from './planLimitScenario'

gooseTest('keeps native Chat tool refusal separate from a plan approval banner', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await expectNoNativeControl(context, { testId: 'plan-approve-btn', relatedControl: () => exerciseGoosePlanLimit(context) })
})
