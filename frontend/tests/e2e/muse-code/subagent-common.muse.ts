/**
 * The shared evidence that Muse 1.4.4 offers no addressable native child session:
 * children run only inside a workflow item's fold, which carries a durable childId
 * and a lifecycle and no session of its own.
 */
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection } from '../helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

/** The word the one fold child of the probe workflow reports. */
const CHILD_WORD = 'FOLDCHILD'

/** The workflow script of the fold probe: one child agent call. */
export function foldChildScript(childInput: string): string {
  return [
    'export default async function workflow(host) {',
    `  const one = await host.agent({ input: ${JSON.stringify(childInput)} });`,
    `  if (!one || one.error_kind) throw new Error('The fold child did not complete.');`,
    `  return { word: ${JSON.stringify(CHILD_WORD)} };`,
    '}',
  ].join('\n')
}

/**
 * Run one workflow with a single child and settle, so the registry holds the
 * workflow row and its fold child. Return the rows and the recorded tool catalog.
 */
export async function runFoldChildWorkflow(native: ManagedNativeScenarioContext, prompt: (text: string) => string) {
  const { page, modelScript } = native
  const catalogRequest = await sendNativeAnswer(native, 'Record the native tool catalog.', 'The native catalog probe completed.')
  const catalog = nativeModelToolNames(catalogRequest)
  const childInput = prompt(`Reply with the single word ${CHILD_WORD}.`)
  await modelScript.rule({
    name: 'the fold child submits its result',
    when: { body: ['Workflow child completion protocol', CHILD_WORD] },
    once: true,
    respond: { toolCalls: [{ id: 'fold-child-submit', name: 'submit_result', namespace: 'muse', arguments: { text: CHILD_WORD, notes: null } }] },
  })
  const start = await modelScript.queue(
    { toolCalls: [codeExecutionToolCall(native.provider, 'muse-fold-child', foldChildScript(childInput))] },
    { text: 'The native workflow completed.' },
  )
  await sendMessage(page, prompt('Run the fold child workflow and report its result.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expandBackgroundTasksSection(page)
  const rows = backgroundTaskRows(page)
  const child = rows.filter({ hasText: /^Child /i }).first()
  await expect(child).toBeVisible()
  return { catalog, rows, child }
}

/** Prove the recorded tool catalog offers no route that addresses a child session. */
export function expectNoChildAddressingTools(catalog: readonly string[]): void {
  expect(catalog, 'the model tool catalog holds no subagent spawn tool').not.toContain('subagent_spawn')
  expect(catalog).not.toContain('subagent_send')
  expect(catalog).not.toContain('subagent_interrupt')
  expect(catalog).not.toContain('task')
}
