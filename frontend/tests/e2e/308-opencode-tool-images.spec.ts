import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

const OPENCODE = AgentProvider.OPENCODE

opencodeTest.describe('OpenCode images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result.
  opencodeTest('a Read of a PNG draws the picture in the tool row', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    const workingDir = authenticatedOpencodeWorkspace.workingDir
    expect(workingDir, 'the agent workspace must expose a working directory').toBeTruthy()
    const name = writeToolImage(workingDir!, 'opencode-33')

    await modelScript.queue(
      { toolCalls: [readToolCall(OPENCODE, 'read-png', join(workingDir!, name))] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectToolRowImage(page, 'tool-image-opencode-33')
  })
})
