import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowWithoutImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

const CLINE = AgentProvider.CLINE

clineTest.describe('Cline images in tool results', () => {
  clineTest('a Read of a PNG draws the name and no picture in the tool row', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    const workingDir = authenticatedClineWorkspace.workingDir
    expect(workingDir, 'the agent workspace must expose a working directory').toBeTruthy()
    const name = writeToolImage(workingDir!, 'cline-19')
    const path = `${workingDir}/${name}`

    await modelScript.queue(
      { toolCalls: [readToolCall(CLINE, 'read-png', path)] },
      { text: `I opened ${name}.` },
    )
    await sendMessage(page, modelScript.prompt(`Read the file ${name} and describe it.`))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The name proves the tool ran. Cline builds tool results as text only, so
    // the row draws no picture (matrix note 3).
    await expectToolRowWithoutImage(page, 'tool-image-cline-19')
  })
})
