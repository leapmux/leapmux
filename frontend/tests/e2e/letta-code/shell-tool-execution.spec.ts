import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatText } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code tool execution', () => {
  lettaTest('draws the output of a command', async ({ native }) => {
    // The command text states no `letta-42`, so only the command's own output can
    // put it on the page.
    const { resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "letta-$((40 + 2))"')],
      prompt: 'Run the arithmetic command.',
      answer: 'The command printed its number.',
    })

    await expect.poll(() => chatText(native.page)).toContain('letta-42')
    // The executor ran the call: its result reached the next model call.
    expect(JSON.stringify(resultRequest.body)).toContain('letta-42')
  })
})

lettaTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
