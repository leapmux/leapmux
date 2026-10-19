import type { Locator, Page } from '@playwright/test'
import type { NativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { exerciseModelRetry } from './nativeModelRetry'

const transcript = vi.hoisted(() => ({ rows: [] as { role: string, text: string }[], stored: [] as { role: string, text: string }[] }))

function locator(read: () => string[]): Locator {
  return {
    read,
    filter: ({ hasText }: { hasText: string | RegExp }) => locator(() => read().filter(text => typeof hasText === 'string' ? text.includes(hasText) : hasText.test(text))),
  } as unknown as Locator
}

vi.mock('./ui', () => ({
  sendMessage: vi.fn(async () => {}),
  waitForAgentIdle: vi.fn(async () => {}),
  assistantBubbles: () => locator(() => transcript.rows.filter(row => row.role === 'agent').map(row => row.text)),
  messageContents: () => locator(() => transcript.rows.map(row => row.text)),
}))

vi.mock('@playwright/test', () => ({
  expect: (value: { read: () => string[] }) => ({
    toHaveCount: async (count: number) => { expect(value.read()).toHaveLength(count) },
    toHaveText: async (text: string) => { expect(value.read()).toEqual([text]) },
  }),
}))

function context() {
  const queue = vi.fn(async () => 4)
  const waitForSteps = vi.fn(async () => {})
  const page = {
    locator: () => locator(() => transcript.rows.filter(row => row.role === 'divider').map(row => row.text)),
    reload: vi.fn(async () => { transcript.rows = [...transcript.stored] }),
  } as unknown as Page
  return {
    context: { page, modelScript: { queue, waitForSteps, prompt: (text: string) => text } } as unknown as NativeScenarioContext,
    queue,
    waitForSteps,
  }
}

beforeEach(() => {
  transcript.rows = [{ role: 'agent', text: 'clean answer' }, { role: 'notification', text: 'API retry succeeded' }, { role: 'divider', text: 'Turn ended' }]
  transcript.stored = [...transcript.rows]
})

describe('exerciseModelRetry', () => {
  const errors = [{ status: 500, message: 'The stream broke.', midStream: true }]

  it('scripts each error before the clean answer and checks the stored transcript', async () => {
    const subject = context()
    await exerciseModelRetry(subject.context, { errors, answer: 'clean answer' })
    expect(subject.queue).toHaveBeenCalledWith({ error: errors[0] }, { text: 'clean answer' })
    expect(subject.waitForSteps).toHaveBeenCalledWith(6)
    expect(subject.context.page.reload).toHaveBeenCalledTimes(1)
  })

  it.each(['live', 'stored'])('rejects retained failed-attempt text in the %s transcript', async (phase) => {
    const rows = phase === 'live' ? transcript.rows : transcript.stored
    rows.push({ role: 'agent', text: 'partial ' })
    await expect(exerciseModelRetry(context().context, { errors, answer: 'clean answer' })).rejects.toThrow()
  })

  it('rejects two retry rows after a successful retry', async () => {
    transcript.rows.push({ role: 'notification', text: 'API retry succeeded' })
    await expect(exerciseModelRetry(context().context, { errors, answer: 'clean answer' })).rejects.toThrow()
  })

  it('rejects an unresolved retry and an intermediate failed divider', async () => {
    transcript.rows[1] = { role: 'notification', text: 'API retry 1' }
    await expect(exerciseModelRetry(context().context, { errors, answer: 'clean answer' })).rejects.toThrow()
    transcript.rows = [...transcript.stored, { role: 'divider', text: 'Turn failed' }]
    await expect(exerciseModelRetry(context().context, { errors, answer: 'clean answer' })).rejects.toThrow()
  })

  it('refuses empty errors or an empty answer before it queues input', async () => {
    const subject = context()
    await expect(exerciseModelRetry(subject.context, { errors: [] })).rejects.toThrow('at least one failed attempt')
    await expect(exerciseModelRetry(subject.context, { errors, answer: ' ' })).rejects.toThrow('nonempty answer')
    expect(subject.queue).not.toHaveBeenCalled()
  })
})
