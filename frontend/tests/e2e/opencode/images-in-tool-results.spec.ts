import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readToolCall } from '../helpers/providerToolCalls'
import { expectToolRowImage, writeToolImage } from '../helpers/toolImages'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

const OPENCODE = AgentProvider.OPENCODE

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
  await waitForAgentIdle(page)

  await expectToolRowImage(page, 'tool-image-opencode-33')
})
