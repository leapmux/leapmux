import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { ACP_SESSION_UPDATE } from './updateVocabulary'
// Side-effect import: the ACP family registers through one shared builder, so any
// one of its providers exercises the shared classifier.
import '../goose/plugin'

/*
 * Every session update the Agent Client Protocol family sends must reach a row the
 * reader can read.
 *
 * The known world is `ACP_SESSION_UPDATE`, the plugin's own table -- the protocol
 * holds no contract of its own for these, because the browser is the only side that
 * dispatches on them. The sweep walks that table and fails for an update that
 * reaches `unknown`, which is the raw-JSON bubble.
 *
 * Five providers share this classifier (Goose, OpenCode, Kilo, Cursor, Reasonix), so
 * an update this sweep misses draws raw JSON on all five at once.
 */

/**
 * The payload each update needs to produce its row.
 *
 * An update carrying nothing to show is hidden on purpose, so a sweep over empty
 * payloads would assert that rule rather than the classification this test is about.
 */
const PAYLOAD: Record<string, Record<string, unknown>> = {
  [ACP_SESSION_UPDATE.TOOL_CALL]: { toolCallId: 'c1', title: 'read', kind: 'read', status: 'pending' },
  [ACP_SESSION_UPDATE.TOOL_CALL_UPDATE]: { toolCallId: 'c1', status: 'completed' },
  [ACP_SESSION_UPDATE.PLAN]: { entries: [{ content: 'Do the thing', status: 'pending' }] },
  [ACP_SESSION_UPDATE.USER_MESSAGE_CHUNK]: { content: { type: 'text', text: 'hello' } },
  [ACP_SESSION_UPDATE.USAGE_UPDATE]: { used: 100, contextLimit: 1000 },
  [ACP_SESSION_UPDATE.SESSION_INFO_UPDATE]: { title: 'a session' },
  [ACP_SESSION_UPDATE.CONFIG_OPTION_UPDATE]: { configId: 'model', value: 'glm-5.3' },
  [ACP_SESSION_UPDATE.AVAILABLE_COMMANDS_UPDATE]: { availableCommands: [{ name: 'review' }] },
}

function classify(sessionUpdate: string): string {
  const plugin = providerFor(AgentProvider.GOOSE)!
  return plugin?.transcript.classify(input({ sessionUpdate, ...PAYLOAD[sessionUpdate] })).kind
}

/**
 * The updates that draw a row of their own rather than a notification or nothing.
 *
 * `user_message_chunk` is NOT one of them. The worker writes the reader's own row
 * from its own record, so the chunk the agent echoes back is hidden rather than
 * drawn a second time.
 */
const STRUCTURAL: Record<string, string> = {
  [ACP_SESSION_UPDATE.TOOL_CALL]: 'tool_use',
  [ACP_SESSION_UPDATE.TOOL_CALL_UPDATE]: 'tool_use',
}

describe('acp session update vocabulary', () => {
  it('reads the whole table the plugin holds', () => {
    // The two text chunks are absent on purpose: the worker joins a run of them into
    // one assembled row, so no chunk reaches a classifier.
    expect(Object.keys(ACP_SESSION_UPDATE).length).toBe(8)
  })

  it.each(Object.values(ACP_SESSION_UPDATE))('names %s', (sessionUpdate) => {
    const kind = classify(sessionUpdate)
    const expected = STRUCTURAL[sessionUpdate]
    if (expected) {
      expect(kind).toBe(expected)
      return
    }
    expect(
      kind,
      `${sessionUpdate} reaches no rule, so the row draws raw JSON on all five ACP `
      + 'providers. Give it a branch in the ACP classifier, or hide it there.',
    ).not.toBe('unknown')
  })

  // An update from a later protocol revision still has to render as something a
  // reader can read. It takes the unknown row, which draws the payload -- the honest
  // answer, and what makes the sweep above worth having.
  it('leaves an update no revision declared as unknown', () => {
    expect(classify('a_update_from_a_later_revision')).toBe('unknown')
  })
})
