import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowWithoutImage, writeToolImage } from './helpers/toolImages'
import { expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

const KIRO = AgentProvider.KIRO

kiroTest.describe('Kiro images in tool results', () => {
  // The agent runs the Allow all policy, so no permission request stands between
  // the scripted call and the row this test reads. The policy is LeapMux's own
  // option, because Kiro never reports its preset.
  //
  // Kiro's read answers an image with metadata text (path, format, size) and no
  // picture. The call runs and names the file. When Kiro returns image content,
  // flip this to `expectToolRowImage` -- LeapMux already renders that result
  // (see 306 and 308).
  kiroTest('a Read of a PNG runs and draws no picture in the tool row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { policyPreset: 'allow-all' })
    const name = writeToolImage(workingDir, 'kiro-58')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'policyPreset-allow-all')

    await modelScript.queue(
      { toolCalls: [readToolCall(KIRO, 'read-png', join(workingDir, name))] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expectToolRowWithoutImage(page, 'tool-image-kiro-58')
  })
})
