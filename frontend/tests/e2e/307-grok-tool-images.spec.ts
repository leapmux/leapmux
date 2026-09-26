import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from './grok-fixtures'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowWithoutImage, writeToolImage } from './helpers/toolImages'
import { expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

const GROK = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build images in tool results', () => {
  // The agent runs Always Approve, so no permission request stands between the
  // scripted call and the row this test reads. The approval mode is LeapMux's
  // own option, because Grok never reports it.
  //
  // The shipped Grok Build 1.0.41 answers "Cannot read binary file" for a valid
  // PNG although its read_file description promises image reads. The call runs
  // and names the file; no picture is drawn. When the CLI returns image
  // content, flip this to `expectToolRowImage` -- LeapMux already renders that
  // result (see 306 and 308).
  grokTest('a Read of a PNG runs and draws no picture in the tool row', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    const name = writeToolImage(workingDir, 'grok-21')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-always-approve')

    await modelScript.queue(
      { toolCalls: [readToolCall(GROK, 'read-png', join(workingDir, name))] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expectToolRowWithoutImage(page, 'tool-image-grok-21')
  })
})
