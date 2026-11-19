import { describe, expect, it } from 'vitest'
import { CODEWHALE_BLOCK_TYPE, CODEWHALE_DEFAULT_MODE, CODEWHALE_ITEM_KIND, CODEWHALE_MODE, CODEWHALE_OPTION, CODEWHALE_POSTURE, CODEWHALE_TOOL, CODEWHALE_TRANSCRIPT_ROLE, CODEWHALE_TURN_STATUS } from '~/generated/contracts/codewhale-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'
import { providerFor } from '../registry'
import { classifyCodewhaleMessage } from './classification'
import { codewhaleControlResponseSummary } from './controlResponse'
import { codewhaleCompactionStart, codewhaleNotificationEntry } from './extractors/notification'
import { codewhaleOutputFilePaths } from './extractors/outputFilePaths'
import { codewhaleResultDivider } from './extractors/resultDivider'
import { codewhaleExtractRow } from './extractors/row'
import { codewhalePermissionPresets } from './permissionPresets'
import { codewhaleConfiguration } from './pluginConfiguration'
import { codewhaleControls } from './pluginControls'
import { codewhaleNeedsRequest, codewhaleSpanRole } from './spanRole'
import { CALL, childBlock, itemFinished, toolCompleted, toolStarted, turnCompleted } from './toolResults.fixtures'
import '~/components/chat/providers'

const plugin = providerFor(AgentProvider.CODEWHALE)!
const SESSION = 'thr-1'

/** One stored message of this provider, in a span when it states one. */
function message(id: string, content: Record<string, unknown>, seq: bigint, span: { spanId?: string, spanType?: string, completion?: MessageCompletion } = {}) {
  return makeTranscriptMessage({
    id,
    provider: AgentProvider.CODEWHALE,
    agentSessionId: SESSION,
    content,
    ...span,
  }, seq)
}

describe('codewhale plugin', () => {
  // Identity, not presence: a hook wired to the wrong reader is defined too.
  it('is registered for the CODEWHALE provider with each hook wired to its own reader', () => {
    expect(plugin.transcript).toStrictEqual({
      spanRole: codewhaleSpanRole,
      needsRequest: codewhaleNeedsRequest,
      classify: classifyCodewhaleMessage,
      extractRow: codewhaleExtractRow,
      notificationEntry: codewhaleNotificationEntry,
      compactionStart: codewhaleCompactionStart,
      extractDivider: codewhaleResultDivider,
      outputFilePaths: codewhaleOutputFilePaths,
    })
    expect(plugin.controls).toBe(codewhaleControls)
    expect(plugin.configuration).toBe(codewhaleConfiguration)
    expect(plugin.controls).toStrictEqual({
      controlResponseDisplay: codewhaleControlResponseSummary,
      permissionPresets: codewhalePermissionPresets,
    })
  })

  // A turn takes text and inline images, and has no file field.
  it('accepts text and images, and refuses a PDF and any other binary', () => {
    expect(plugin.configuration?.attachments).toStrictEqual({
      text: true,
      image: true,
      pdf: false,
      audio: false,
      binary: false,
    })
  })

  it('drives the plan toggle and the trigger segment from the thread mode', () => {
    const configuration = plugin.configuration!
    expect(configuration.triggerModeGroupKey).toBe(CODEWHALE_OPTION.Mode)
    expect(configuration.planMode?.groupKey).toBe(CODEWHALE_OPTION.Mode)
    expect(configuration.planMode?.planValue).toBe(CODEWHALE_MODE.Plan)
    expect(configuration.planMode?.defaultValue).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({})).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({ optionValues: {} })).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({
      optionValues: { permissionMode: CODEWHALE_POSTURE.FullAccess },
    })).toBe(CODEWHALE_DEFAULT_MODE)
    expect(configuration.planMode?.currentMode({
      optionValues: { [CODEWHALE_OPTION.Mode]: CODEWHALE_MODE.Plan },
    })).toBe(CODEWHALE_MODE.Plan)
  })

  it('maps the two permission presets onto the runtime\'s postures', () => {
    expect(plugin.controls?.permissionPresets).toStrictEqual({
      smart: {
        sets: { permissionMode: CODEWHALE_POSTURE.AutoReview },
      },
      bypass: {
        sets: { permissionMode: CODEWHALE_POSTURE.FullAccess },
      },
    })
  })
})

describe('codewhale transcript through the whole pipeline', () => {
  it('pairs a command\'s two rows into one call', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('request', toolStarted(CODEWHALE_TOOL.Bash, { command: 'ls' }), 1n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash }),
        message('result', toolCompleted(CODEWHALE_TOOL.Bash, { command: 'ls' }, 'a.ts\n', { exit_code: 0 }), 2n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash }),
      ],
    })
    const requestRow = scenario.toolRow('request')
    const resultRow = scenario.toolRow('result')
    expect(requestRow.role).toBe('request')
    expect(resultRow.role).toBe('result')
    expect(resultRow.hasRequestRow).toBe(true)
    expect(resultRow.call.kind).toBe('execute')
    expect(resultRow.call.status).toBe('completed')
    // The request row reads its own opening frame alone.
    expect(requestRow.call.status).toBe('in_progress')
    expect(requestRow.call.result).toBeUndefined()
  })

  it('states a deferred tool\'s first call on its result row, with the runtime\'s words', () => {
    const words = 'Tool `apply_patch` was deferred and has now been loaded.'
    const scenario = createTranscriptScenario({
      archive: [
        message('first-request', toolStarted(CODEWHALE_TOOL.ApplyPatch, { patch: 'x' }), 1n, { spanId: CALL, spanType: CODEWHALE_TOOL.ApplyPatch }),
        message('first-result', toolCompleted(CODEWHALE_TOOL.ApplyPatch, { patch: 'x' }, words, { deferred_tool_loaded: true }), 2n, { spanId: CALL, spanType: CODEWHALE_TOOL.ApplyPatch }),
      ],
    })
    // Only the result row states the runtime's words, so the classifier keeps it.
    expect(scenario.toolRow('first-result').call.result).toStrictEqual({ unparsed: true, text: words })
    expect(scenario.toolRow('first-request').call.result).toBeUndefined()
  })

  it('states a call its turn cut short as cancelled', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('request', toolStarted(CODEWHALE_TOOL.Bash, { command: 'sleep 9' }), 1n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash }),
        message('retained', toolStarted(CODEWHALE_TOOL.Bash, { command: 'sleep 9' }), 2n, { spanId: CALL, spanType: CODEWHALE_TOOL.Bash, completion: MessageCompletion.INTERRUPTED }),
      ],
    })
    expect(scenario.toolRow('retained').role).toBe('result')
    expect(scenario.toolRow('retained').call.status).toBe('cancelled')
  })

  it('reads a subagent\'s transcript rows', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('thinking', childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Thinking, thinking: 'Child thinking.' }), 1n),
        message('use', childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, {
          type: CODEWHALE_BLOCK_TYPE.ToolUse,
          id: 'b1',
          name: CODEWHALE_TOOL.Bash,
          input: { command: 'ls' },
        }), 2n, { spanId: 'b1', spanType: CODEWHALE_TOOL.Bash }),
        message('result', childBlock(CODEWHALE_TRANSCRIPT_ROLE.User, { type: CODEWHALE_BLOCK_TYPE.ToolResult, tool_use_id: 'b1', content: 'a.ts' }, 3), 3n, { spanId: 'b1', spanType: CODEWHALE_TOOL.Bash }),
        message('text', childBlock(CODEWHALE_TRANSCRIPT_ROLE.Assistant, { type: CODEWHALE_BLOCK_TYPE.Text, text: 'There is 1 file.' }, 4), 4n),
      ],
    })
    expect(scenario.extract('thinking')).toMatchObject({
      kind: 'row',
      row: { kind: 'assistant-thinking', text: 'Child thinking.' },
    })
    expect(scenario.toolRow('result').call).toMatchObject({
      kind: 'execute',
      status: 'completed',
      request: { command: 'ls' },
    })
    expect(scenario.extract('text')).toMatchObject({
      kind: 'row',
      row: { kind: 'assistant-text', text: 'There is 1 file.' },
    })
  })

  it('reads a reply, a turn end and a notice', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('reply', itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hello there.'), 1n),
        message('status', itemFinished(CODEWHALE_ITEM_KIND.Status, 'Checkpoint saved'), 2n),
        message('end', turnCompleted(CODEWHALE_TURN_STATUS.Completed, { duration_ms: 1500 }), 3n),
      ],
    })
    expect(scenario.extract('reply')).toMatchObject({
      kind: 'row',
      row: { kind: 'assistant-text', text: 'Hello there.' },
    })
    expect(scenario.extract('status')).toMatchObject({
      kind: 'row',
      row: {
        kind: 'notification',
        thread: {
          entries: [
            { kind: 'status', text: 'Checkpoint saved' },
          ],
        },
      },
    })
    expect(scenario.extract('end')).toMatchObject({
      kind: 'row',
      row: {
        kind: 'divider',
        divider: { label: 'Turn ended (1.5s)' },
      },
    })
  })
})

describe('codewhale span roles', () => {
  it('asks for the request of a result row alone', () => {
    const scenario = createTranscriptScenario({
      archive: [
        message('request', toolStarted(CODEWHALE_TOOL.Bash, {}), 1n, { spanId: CALL }),
      ],
    })
    const resolved = scenario.entry('request').resolved
    expect(plugin.transcript.spanRole(resolved)).toBe('request')
    expect(plugin.transcript.needsRequest?.(resolved)).toBe(false)
    const other = createTranscriptScenario({
      archive: [
        message('result', toolCompleted(CODEWHALE_TOOL.Bash, {}, 'x'), 1n, { spanId: CALL }),
      ],
    })
    const result = other.entry('result').resolved
    expect(plugin.transcript.spanRole(result)).toBe('result')
    expect(plugin.transcript.needsRequest?.(result)).toBe(true)
    const reply = createTranscriptScenario({
      archive: [
        message('reply', itemFinished(CODEWHALE_ITEM_KIND.AgentMessage, 'Hi'), 1n),
      ],
    })
    expect(plugin.transcript.spanRole(reply.entry('reply').resolved)).toBe('other')
    expect(plugin.transcript.needsRequest?.(reply.entry('reply').resolved)).toBe(false)
  })
})
