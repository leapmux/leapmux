import type { DescMethod } from '@bufbuild/protobuf'
import type { Locator } from '@playwright/test'
import { describe, expect, it, vi } from 'vitest'
import { WorkerAgentService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { procedureOf } from '../../../src/lib/rpcProcedure'
import { copySettingsNotification, expectSettingsNotificationEnvelope } from './notificationCopy'

/** The JSON that the fake Copy button writes to the clipboard: one settings notification. */
const copy = vi.hoisted(() => ({
  text: JSON.stringify({
    content: {
      type: 'settings_changed',
      changes: {
        mode: { old: 'default', new: 'plan' },
      },
    },
  }),
}))

vi.mock('./clipboardCapture', () => ({
  captureClipboardText: async (_page: unknown, action: () => Promise<unknown>) => {
    await action()
    return copy.text
  },
}))

vi.mock('./timingFixture', () => ({
  installRpcListeners: async () => {},
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (value: unknown, message?: string) => {
      // The fake bubble is one row on the page.
      if (typeof value === 'object' && value !== null && 'fakeBubble' in value) {
        return {
          toHaveCount: async (count: number) => {
            expect(count).toBe(1)
          },
        }
      }
      return expect(value, message)
    },
  }
})

describe('expectSettingsNotificationEnvelope', () => {
  const change = { groupId: 'mode', oldValue: 'default', value: 'plan' }
  const entry = {
    type: 'settings_changed',
    changes: {
      mode: { old: 'default', new: 'plan' },
    },
  }
  const thread = {
    type: 'notification_thread',
    messages: [entry],
  }

  it('accepts the original mode transition in a consolidated thread and preserves an object supplement', () => {
    expectSettingsNotificationEnvelope(JSON.stringify({
      content: thread,
      supplemental_content: { metadata: {} },
    }), change, true)
  })

  it('accepts a single native notification when consolidation is not required', () => {
    expectSettingsNotificationEnvelope(JSON.stringify({ content: entry }), change)
  })

  it('accepts the null supplement that Copy emits when the notification has none', () => {
    expectSettingsNotificationEnvelope(JSON.stringify({ content: thread, supplemental_content: null }), change, true)
  })

  it('refuses a single entry when the scenario requires consolidation', () => {
    expect(() => expectSettingsNotificationEnvelope(JSON.stringify({ content: entry }), change, true)).toThrow('Copy must return a consolidated notification thread.')
  })

  it.each([
    [null, 'Copy must return a notification envelope with object content.'],
    [{}, 'Copy must return a notification envelope with object content.'],
    [
      { content: null },
      'Copy must return a notification envelope with object content.',
    ],
    [
      {
        content: { type: 'notification_thread' },
      },
      'The copied notification thread must contain its messages.',
    ],
    [
      {
        content: { type: 'notification_thread', messages: [] },
      },
      'Copy must contain exactly one settings notification.',
    ],
  ] as const)('rejects an absent notification: %j', (envelope, reason) => {
    expect(() => expectSettingsNotificationEnvelope(JSON.stringify(envelope), change)).toThrow(reason)
  })

  it('rejects duplicate settings entries instead of accepting the first one', () => {
    expect(() => expectSettingsNotificationEnvelope(JSON.stringify({
      content: {
        ...thread,
        messages: [entry, entry],
      },
    }), change)).toThrow('Copy must contain exactly one settings notification.')
  })

  it.each([
    {
      mode: { old: 'other', new: 'plan' },
    },
    {
      mode: { old: 'default', new: 'other' },
    },
    {
      other: { old: 'default', new: 'plan' },
    },
  ])('rejects an incorrect setting transition: %j', (changes) => {
    expect(() => expectSettingsNotificationEnvelope(JSON.stringify({
      content: { ...entry, changes },
    }), change)).toThrow('Copy must preserve the original setting transition.')
  })

  it.each([
    ['opaque', 'The copied supplement must be an object.'],
    [0, 'The copied supplement must be an object.'],
    [[], 'The copied supplement must be an object.'],
    [
      { metadata: null },
      'The copied supplement metadata must be an object.',
    ],
    [
      {
        metadata: { notification_entries: [] },
      },
      'Copy must not synthesize notification entries.',
    ],
    [
      {
        metadata: { notification_reduction: {} },
      },
      'Copy must not synthesize notification reduction.',
    ],
  ] as const)('rejects a malformed or synthesized supplement: %j', (supplement, reason) => {
    expect(() => expectSettingsNotificationEnvelope(JSON.stringify({ content: thread, supplemental_content: supplement }), change)).toThrow(reason)
  })
})

describe('copySettingsNotification', () => {
  const change = { groupId: 'mode', oldValue: 'default', value: 'plan' }

  /** A settings row whose page observed `sent`, as the RPC listener records the procedures that the app sends. */
  function bubble(sent: string[]): Locator {
    const page = {
      evaluate: async () => sent,
    }
    const row = {
      fakeBubble: true,
      page: () => page,
      hover: async () => {},
      locator: () => ({
        getByTestId: () => ({
          click: async () => {},
        }),
      }),
    }
    return row as unknown as Locator
  }

  it('accepts a copy whose page fetched no message history', async () => {
    await expect(copySettingsNotification(bubble([
      procedureOf(WorkerAgentService.method.listAgents),
    ]), change)).resolves.toBeUndefined()
  })

  // The app's worker RPC layer emits each call under procedureOf(method), so the check compares that form.
  it.each([
    WorkerAgentService.method.listAgentMessages,
    WorkerAgentService.method.getAgentMessage,
    WorkerAgentService.method.getAgentSpanMessages,
  ] as DescMethod[])('fails when the copy fetched history through $name', async (method) => {
    await expect(copySettingsNotification(bubble([
      procedureOf(method),
    ]), change)).rejects.toThrow('to not include')
  })
})
