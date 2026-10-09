import { expect } from '@playwright/test'
import { INTERRUPTION_MARKER, parseAssembledMessage } from '../../../src/components/chat/assembledMessage'
import { mimoToolPart } from '../../../src/components/chat/providers/mimo/extractors/toolCommon'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bandRows } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest('interrupts an actual held native turn and keeps its session usable during a model call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'model' })
})

mimoTest('interrupts an actual held native turn and keeps its session usable during a tool call', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})

mimoTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})

mimoTest('withdraws a waiting question and keeps its session usable', async ({ native }) => {
  await exerciseControlInterrupt(native, { control: 'question' })
})

mimoTest('keeps completed reasoning before a stopped native tool in its original order after reload', async ({ native }) => {
  const reasoning = 'The native thought completes before the held shell operation starts.'
  const agent = await currentNativeAgent(native)
  const proveThought = async () => {
    const snapshot = await readNativeMessageSnapshot(native, agent.id)
    const thoughts = snapshot.messages.filter(message => parseAssembledMessage(nativeMessageBody(message))?.text === reasoning)
    expect(thoughts).toHaveLength(1)
    const thought = thoughts[0]
    if (!thought)
      throw new Error('The native tool turn supplied no saved reasoning row.')
    expect(parseAssembledMessage(nativeMessageBody(thought))).toEqual({ kind: 'reasoning', text: reasoning, completion: 'complete' })
    const tools = snapshot.messages.filter(message => mimoToolPart(nativeMessageBody(message))?.callId === 'held-native-tool')
    expect(tools.length).toBeGreaterThan(0)
    expect(tools.every(message => message.seq > thought.seq)).toBe(true)
  }
  await exerciseInterruptTurn(native, { kind: 'tool', reasoning, beforeInterrupt: proveThought })
  for (const reload of [false, true]) {
    if (reload)
      await native.page.reload()
    await proveThought()
    const thought = bandRows(native.page, 'thought').filter({ hasText: reasoning })
    await expect(thought).toHaveCount(1)
    await expect(thought).not.toContainText(INTERRUPTION_MARKER)
  }
})
