import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { readToolCall } from './helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from './helpers/toolImages'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

const CLAUDE = AgentProvider.CLAUDE_CODE

test.describe('Claude Code images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result, so the `img`
  // is not something the prompt or the scripted reply can fake.
  test('a Read of a PNG draws the picture in the tool row', async ({ authenticatedWorkspace, page, modelScript }) => {
    const workingDir = authenticatedWorkspace.workingDir
    expect(workingDir, 'the agent workspace must expose a working directory').toBeTruthy()
    const name = writeToolImage(workingDir!, 'claude-42')

    await modelScript.queue(
      { toolCalls: [readToolCall(CLAUDE, 'read-png', name)] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectToolRowImage(page, 'tool-image-claude-42')
  })
})
