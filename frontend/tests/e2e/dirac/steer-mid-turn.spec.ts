import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect as diracExpect, diracTest, openDiracAgent } from '../dirac-fixtures'
import { bashToolCall, diracRespondToolCall } from '../helpers/providerToolCalls'
import { expectSteeredReply, steerQueuedInput } from '../helpers/steer'
import { openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

diracTest.describe('Dirac model and steering', () => {
  diracTest('puts a native whisper into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const gate = 'dirac-whisper-gate'
    const steering = 'Include STEEREDWORD in the answer.'
    await modelScript.queue(
      { gate, toolCalls: [bashToolCall(AgentProvider.DIRAC, 'dirac-steer-tool', 'printf dirac-steer-ready')] },
      { toolCalls: [diracRespondToolCall('dirac-steer-answer', 'complete', 'The answer includes STEEREDWORD.')] },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command, then answer.'))
    await modelScript.waitForGate(gate)
    try {
      await steerQueuedInput(page, { message: steering, match: 'Include STEEREDWORD' })
    }
    finally {
      await modelScript.releaseGate(gate)
    }
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    diracExpect(JSON.stringify(status.requests.find(request => request.stepIndex === 1)?.body)).toContain(steering)
    await expectSteeredReply(page, 'STEEREDWORD', 'last')
  })
})
