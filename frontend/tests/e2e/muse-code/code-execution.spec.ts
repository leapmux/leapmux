/**
 * The model supplies JavaScript source to Muse's native workflow executor.
 *
 * The script runs in the native host, returns computed output, and its thrown error
 * reaches the same tool row. Ordinary shell tools are not code execution; the workflow
 * tool is Muse's own source-execution interface.
 */
import { expect } from '@playwright/test'
import { codeExecutionToolCall } from '../helpers/providerToolCalls'
import { chatScrollContainer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

/** The output script: the host computes and returns the marker and the sum. */
function outputScript(marker: string): string {
  return `export default async function workflow(host) { await host.agent({ input: ${JSON.stringify(`Compute ${marker}.`)} }); return { marker: ${JSON.stringify(marker)} + (40 + 2) }; }`
}

/** The failure script: the host throws the marker and the sum inside the error. */
function failureScript(marker: string): string {
  return `export default async function workflow(host) { await host.agent({ input: ${JSON.stringify(`Fail ${marker}.`)} }); throw new Error(${JSON.stringify(marker)} + (70 + 7)); }`
}

museTest('runs native workflow source and retains its output and error after reload', async ({ native }) => {
  const { page, modelScript } = native
  const outputMarker = 'MUSE_WORKFLOW_OUTPUT'
  const failureMarker = 'MUSE_WORKFLOW_FAILURE'
  await modelScript.rule(
    { name: 'the compute child answers', when: { user: `Compute ${outputMarker}.` }, respond: { text: 'Computed.' }, once: true },
    { name: 'the failure child answers', when: { user: `Fail ${failureMarker}.` }, respond: { text: 'Failing.' }, once: true },
  )
  const outputStart = await modelScript.queue(
    { toolCalls: [codeExecutionToolCall(native.provider, 'muse-native-code', outputScript(outputMarker))] },
    { text: 'The native workflow returned its marker.' },
  )
  await sendMessage(page, modelScript.prompt('Run the native output workflow and report its result.'))
  await modelScript.waitForSteps(outputStart + 2)
  await waitForAgentIdle(page)

  const failureStart = await modelScript.queue(
    { toolCalls: [codeExecutionToolCall(native.provider, 'muse-native-code-failure', failureScript(failureMarker))] },
    { text: 'The native workflow threw its marker.' },
  )
  await sendMessage(page, modelScript.prompt('Run the native failure workflow and report its error.'))
  await modelScript.waitForSteps(failureStart + 2)
  await waitForAgentIdle(page)

  await expect(chatScrollContainer(page).getByText(new RegExp(`${outputMarker}42`))).toBeVisible()
  await expect(chatScrollContainer(page).getByText(new RegExp(`${failureMarker}77`))).toBeVisible()
  await page.reload()
  await waitForAgentIdle(page)
  await expect(chatScrollContainer(page).getByText(new RegExp(`${outputMarker}42`))).toBeVisible()
  await expect(chatScrollContainer(page).getByText(new RegExp(`${failureMarker}77`))).toBeVisible()
})

museTest('delivers the native workflow result to the next model request', async ({ native }) => {
  const { page, modelScript } = native
  const marker = 'MUSE_WORKFLOW_RECEIPT'
  await modelScript.rule({
    name: 'the receipt child submits its result',
    when: { body: ['Workflow child completion protocol', marker] },
    once: true,
    respond: { toolCalls: [{ id: 'receipt-child-submit', name: 'submit_result', namespace: 'muse', arguments: { text: 'Computed.', notes: null } }] },
  })
  const start = await modelScript.queue(
    { toolCalls: [codeExecutionToolCall(native.provider, 'muse-native-code-receipt', outputScript(marker))] },
    { text: 'The native workflow launched.' },
  )
  await sendMessage(page, modelScript.prompt('Run the native receipt workflow and report its result.'))
  // The workflow completes in the background and its reconciliation turn carries
  // the computed result back to the model.
  await expect.poll(async () => {
    const status = await modelScript.status()
    return status.requests.some(request => (JSON.stringify(request.body) ?? '').includes(`${marker}42`))
  }, { timeout: 60000 }).toBe(true)
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
})
