import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest, expect } from './codex-fixtures'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

const CODEX = AgentProvider.CODEX

codexTest.describe('Codex images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result.
  codexTest('a Read of a PNG draws the picture in the tool row', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir, 'the agent workspace must expose a working directory').toBeTruthy()
    const name = writeToolImage(workingDir!, 'codex-77')

    await modelScript.queue(
      { toolCalls: [readToolCall(CODEX, 'read-png', name)] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectToolRowImage(page, 'tool-image-codex-77')
  })
})
