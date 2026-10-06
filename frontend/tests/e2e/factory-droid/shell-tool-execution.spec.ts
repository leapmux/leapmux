import { droidTest, expect } from '../droid-fixtures'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chatText } from '../helpers/ui'

droidTest.describe('Factory Droid tool execution', () => {
  droidTest('draws the output of a command', async ({ native }) => {
    // The command text states no `droid-42`, so only the command's own output can
    // put it on the page.
    const { resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "droid-$((40 + 2))"')],
      prompt: 'Run the arithmetic command.',
      answer: 'The command printed its number.',
    })

    await expect.poll(() => chatText(native.page)).toContain('droid-42')
    // The executor ran the call: its result reached the next model call.
    expect(JSON.stringify(resultRequest.body)).toContain('droid-42')
  })
})

droidTest('runs successful and failed native commands with their actual output', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
