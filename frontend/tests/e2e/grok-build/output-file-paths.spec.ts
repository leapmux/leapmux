import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { computedNativeToolOutput } from '../helpers/nativeToolOutput'
import { proveNativeOutputReceipt } from '../helpers/nativeToolOutputFilePaths'
import { captureNativeToolOutput } from '../helpers/nativeToolOutputScenario'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { readGrokNativeOutput } from './outputFilePaths'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest('keeps the native filesystem path and exact inline preview after reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
  const opened = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  expect((await currentNativeAgent(context)).id).toBe(opened.agentId)
  await captureNativeToolOutput(context, testInfo, {
    output: computedNativeToolOutput({ lineCount: 6000, padding: 48 }),
    callId: 'native-grok-build-output-path',
    proof: capture => proveNativeOutputReceipt(capture, testInfo, readGrokNativeOutput),
  })
})
