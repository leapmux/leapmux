import { describe, expect, it, vi } from 'vitest'
import { MCP_ELICITATION_ACTION } from '~/generated/contracts/mcp-elicitation'
import { MIMO_DEFAULT_MODE, MIMO_MODE, MIMO_OPTION, MIMO_PERMISSION_POLICY, MIMO_STATUS_TYPE, MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { OPENCODE_EVENT } from '~/generated/contracts/opencode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { assembledMessageRow } from '~/test-support/assembledMessages'
import { mimoFrame, openingFrame, parsedFrame, statusFrame, TEST_SESSION, toolFrame } from '~/test-support/mimoFixtures'
import { providerQuotableText } from '~/test-support/toolCallFixture'
import { buildDenyResponse } from '~/utils/controlResponse'
import { controlSurface } from '../../controls/controlSurface'
import { buildElicitationResponse } from '../../controls/elicitationForm'
import { createControlAnswerState } from '../../controls/types'
import { providerFor } from '../registry'

// Side-effect import to register the MiMo plugin.
import './plugin'

const plugin = providerFor(AgentProvider.MIMO_CODE)!

describe('mimo plugin metadata', () => {
  it('is registered for the MIMO_CODE provider', () => {
    expect(plugin).toBeDefined()
  })

  // The worker's ValidateAttachment accepts the same three kinds. A file of another
  // type would reach the model's API unconverted, so both sides refuse it.
  it('advertises text, image and PDF attachments, and no binary', () => {
    expect(plugin.configuration?.attachments).toEqual({ text: true, image: true, pdf: true, binary: false })
  })

  it('carries its primary agents on the permission-mode channel', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode).toMatchObject({
      groupKey: 'permissionMode',
      planValue: MIMO_MODE.Plan,
      defaultValue: MIMO_DEFAULT_MODE,
    })
  })

  it('reads the current primary agent, and falls back to the default before the first report', () => {
    const currentMode = plugin.configuration!.planMode!.currentMode
    expect(currentMode({ optionValues: { permissionMode: MIMO_MODE.Plan } })).toBe(MIMO_MODE.Plan)
    expect(currentMode({ optionValues: {} })).toBe(MIMO_DEFAULT_MODE)
    expect(currentMode({})).toBe(MIMO_DEFAULT_MODE)
  })

  // MiMo has no permission mode. Bypass turns on its own permission policy, which
  // approves every call, deletes included.
  it('maps the bypass preset onto the permission policy', () => {
    expect(plugin.controls?.permissionPresets).toEqual({
      bypass: { sets: { [MIMO_OPTION.PermissionPolicy]: MIMO_PERMISSION_POLICY.Bypass } },
    })
  })

  it('lets the reader send a message to a subagent', () => {
    expect(plugin.configuration?.supportsSubagentSend).toBe(true)
  })

  // A MiMo session id is an opaque token that the server mints, not a file.
  it('does not treat the session id as a file path', () => {
    expect(plugin.session?.sessionIdIsFilePath).toBeFalsy()
  })
})

describe('mimo plugin controls', () => {
  it('builds a deny envelope for a composer send, including an empty one', () => {
    expect(plugin.controls?.buildControlResponse!({}, '', 'req-1')).toEqual(buildDenyResponse('req-1', ''))
    expect(plugin.controls?.buildControlResponse!({}, 'not yet', 'req-1')).toEqual(buildDenyResponse('req-1', 'not yet'))
  })

  it('sends the chosen permission option as MiMo\'s own reply word', async () => {
    const send = vi.fn().mockResolvedValue(undefined)
    await plugin.controls!.sendPermissionOption!(send, 'mimo-permission:per_1', 'always')
    expect(JSON.parse(new TextDecoder().decode(send.mock.calls[0]?.[0]))).toEqual({
      jsonrpc: '2.0',
      id: 'mimo-permission:per_1',
      result: { outcome: { outcome: 'selected', optionId: 'always' } },
    })
  })

  it('routes a question to the question card and a plan approval to the plan card', () => {
    const question = { type: OPENCODE_EVENT.QuestionAsked, properties: { id: 'que_1', questions: [{ question: 'Which?' }] }, request: { tool_name: MIMO_TOOL.Question } }
    // The browser reads the tool name the worker recorded, not the question key,
    // so the key here is only the one MiMo sends.
    const plan = { type: OPENCODE_EVENT.QuestionAsked, properties: { id: 'que_2', questions: [{ key: 'plan_exit' }] }, request: { tool_name: MIMO_TOOL.PlanExit }, plan: 'Do it' }
    expect(plugin.controls?.askUserQuestion?.isRequest(question)).toBe(true)
    expect(plugin.controls?.askUserQuestion?.isRequest(plan)).toBe(false)
    expect(plugin.controls?.extractControl!({ payload: plan })).toEqual({ kind: 'plan', text: 'Do it' })
  })

  describe('an MCP elicitation', () => {
    const elicitation = {
      type: OPENCODE_EVENT.QuestionAsked,
      properties: {
        id: 'que_3',
        questions: [{
          key: 'mcp_elicitation',
          header: 'docs',
          question: 'docs\n\nProceed?',
          options: [{ label: 'Accept', description: '' }, { label: 'Decline', description: '' }, { label: 'Cancel', description: '' }],
          multiple: false,
          custom: false,
        }],
      },
      request: { tool_name: MIMO_TOOL.Question },
    }

    // MiMo asks an MCP server's confirmation as a question that takes no free text,
    // and it reads every answer but Accept and Decline as Cancel. The question card
    // would offer free text and YOLO, which MiMo reads as Cancel, so the shared
    // elicitation form draws it instead.
    it('goes to the elicitation form, not the question card', () => {
      expect(plugin.controls?.askUserQuestion?.isRequest(elicitation)).toBe(false)
      expect(controlSurface({ requestId: 'r', agentId: 'a', payload: elicitation }, AgentProvider.MIMO_CODE, undefined)).toEqual({
        kind: 'elicitation',
        elicitation: { mode: 'form', server: 'docs', message: 'Proceed?', schema: { type: 'object', properties: {} } },
      })
    })

    it('states the saved answer in the elicitation words', () => {
      const display = plugin.controls!.controlResponseDisplay!
      const saved = (response: Record<string, unknown>) => ({ requestId: 'r', claimToken: 't', request: elicitation, response })
      expect(display(saved(buildElicitationResponse('r', MCP_ELICITATION_ACTION.Accept, {})))).toEqual({ kind: 'label', text: 'Approved' })
      expect(display(saved(buildElicitationResponse('r', MCP_ELICITATION_ACTION.Decline)))).toEqual({ kind: 'label', text: 'Rejected' })
      expect(display(saved(buildElicitationResponse('r', MCP_ELICITATION_ACTION.Cancel)))).toEqual({ kind: 'label', text: 'Cancelled' })
    })

    it('leaves the saved answer to a question in the question words', () => {
      const question = { type: OPENCODE_EVENT.QuestionAsked, properties: { id: 'que_1', questions: [{ question: 'Which?', header: 'Pick' }] }, request: { tool_name: MIMO_TOOL.Question } }
      expect(plugin.controls!.controlResponseDisplay!({ requestId: 'r', claimToken: 't', request: question, response: { result: { answers: [['A']] } } }))
        .toEqual({ kind: 'label', text: 'Pick: A' })
    })
  })

  // MiMo's question tool is OpenCode's: the event, the answers and the rejection are
  // the ones the shared OpenCode question wire reads and writes.
  describe('a question', () => {
    const question = {
      type: OPENCODE_EVENT.QuestionAsked,
      properties: {
        id: 'que_1',
        questions: [
          { question: 'Which database?', header: 'Database', options: [{ label: 'SQLite' }, { label: 'Postgres' }], multiple: true },
          { question: 'Anything else?', options: [] },
        ],
      },
      request: { tool_name: MIMO_TOOL.Question },
    }
    const request = { requestId: 'mimo-question:que_1', agentId: 'a', payload: question }
    const sentBody = (send: ReturnType<typeof vi.fn>): unknown => JSON.parse(new TextDecoder().decode(send.mock.calls[0]?.[0]))

    it('folds MiMo\'s multiple onto multiSelect', () => {
      const questions = plugin.controls!.askUserQuestion!.extractQuestions(question)
      expect(questions).toHaveLength(2)
      expect(questions[0]).toMatchObject({ question: 'Which database?', header: 'Database', multiSelect: true })
      expect(questions[1]?.options).toEqual([])
    })

    it('answers each question with its choices or the typed words', async () => {
      const send = vi.fn().mockResolvedValue(undefined)
      const questions = plugin.controls!.askUserQuestion!.extractQuestions(question)
      const state = createControlAnswerState({ selections: { 0: ['SQLite', 'Postgres'] }, customTexts: { 1: '  more tests  ' } })
      await plugin.controls!.askUserQuestion!.sendAnswer(request, send, questions, state)
      expect(sentBody(send)).toEqual({ jsonrpc: '2.0', id: 'mimo-question:que_1', result: { answers: [['SQLite', 'Postgres'], ['more tests']] } })
    })

    // MiMo's reject route takes no body, so the reader's words cannot reach it.
    it('dismisses the question, without the reader\'s words', async () => {
      const send = vi.fn().mockResolvedValue(undefined)
      await plugin.controls!.askUserQuestion!.sendReject(request, send, 'not now')
      expect(sentBody(send)).toEqual({ jsonrpc: '2.0', id: 'mimo-question:que_1', result: { rejected: true } })
    })
  })
})

// The transcript hooks the plugin registers are MiMo's own readers: each answers a
// MiMo frame, which a reader of another provider would not.
describe('mimo plugin transcript', () => {
  it('reads the turn end and the retry of a MiMo frame', () => {
    expect(plugin.transcript.extractDivider(statusFrame(MIMO_STATUS_TYPE.Idle))).toEqual({ label: 'Turn ended' })
    expect(plugin.transcript.notificationEntry?.(statusFrame(MIMO_STATUS_TYPE.Retry, { attempt: 1 }))).toEqual([{ kind: 'retry', scope: 'api', attempt: 1 }])
  })

  it('pairs the two halves of a MiMo tool call', () => {
    const opening = parsedFrame(openingFrame(MIMO_TOOL.Bash, { command: 'ls' }))
    const final = parsedFrame(toolFrame(MIMO_TOOL.Bash, { input: { command: 'ls' } }))
    expect(plugin.transcript.spanRole(opening)).toBe('request')
    expect(plugin.transcript.spanRole(final)).toBe('result')
    expect(plugin.transcript.relatedMessages?.(opening)).toEqual(['result'])
    expect(plugin.transcript.classify(final)).toEqual({ kind: 'tool_result' })
  })
})

describe('mimo quotable text', () => {
  it('reads the user\'s words and the assembled assistant text', () => {
    expect(providerQuotableText(AgentProvider.MIMO_CODE, { content: 'hi' }, { category: { kind: 'user_content' } })).toBe('hi')
    expect(providerQuotableText(AgentProvider.MIMO_CODE, assembledMessageRow('text', 'Hello'), { category: { kind: 'assistant_text' } })).toBe('Hello')
    expect(providerQuotableText(AgentProvider.MIMO_CODE, assembledMessageRow('reasoning', 'thinking'), { category: { kind: 'assistant_thinking' } })).toBe('thinking')
  })

  it('reads nothing from a hidden status frame', () => {
    expect(plugin.transcript.classify(parsedFrame(statusFrame(MIMO_STATUS_TYPE.Busy)))).toEqual({ kind: 'hidden' })
    expect(providerQuotableText(AgentProvider.MIMO_CODE, statusFrame(MIMO_STATUS_TYPE.Busy))).toBeNull()
  })

  // The worker persists only the events that the plugin reads. Any other event is a
  // shape that nobody expected, so it stays visible as an unknown row.
  it('classifies an event that no reader knows as unknown', () => {
    expect(plugin.transcript.classify(parsedFrame(mimoFrame('session.diff', { sessionID: TEST_SESSION })))).toEqual({ kind: 'unknown' })
  })
})
