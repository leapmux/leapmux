import { claudeTest } from '../claude-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectPngInRequest, expectToolRowImage, runToolImageTurn } from '../helpers/toolImages'

claudeTest.describe('Claude Code images in tool results', () => {
  // The mock scripts the Read call. The picture in the tool row is produced by
  // the CLI reading the PNG and by LeapMux rendering that result, so the `img`
  // is not something the prompt or the scripted reply can fake.
  claudeTest('a Read of a PNG draws the picture in the tool row', async ({ native, authenticatedClaudeWorkspace }) => {
    const { resultRequest } = await runToolImageTurn(native, {
      workingDir: authenticatedClaudeWorkspace.workingDir,
      marker: 'claude-42',
      toolCall: image => readToolCall(native.provider, 'read-png', image.fileName),
    })
    // An Anthropic tool result carries the image block of the Read, so the next model request holds the PNG.
    expectPngInRequest(resultRequest)
    await expectToolRowImage(native.page, 'tool-image-claude-42')
  })
})
