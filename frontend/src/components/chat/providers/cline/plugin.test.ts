import type { ControlPrompt } from '../../model/controlPrompt'
import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { CLINE_DECLINE_REASON, CLINE_PERMISSION_MODE } from '~/generated/contracts/cline-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { providerFor } from '../registry'
import { classifyClineMessage } from './classification'
import { clineCompactionBoundary, clineCompactionStart, clineNotificationEntry } from './extractors/notification'
import { clineResultDivider } from './extractors/resultDivider'
import { clineExtractRow } from './extractors/row'
import { clineConfiguration } from './pluginConfiguration'
import { clineControls } from './pluginControls'
import { clineNeedsRequest, clineSpanRole } from './spanRole'
import './plugin'

const plugin = providerFor(AgentProvider.CLINE)!
const controls = plugin.controls!

/** A stored approval request, as the worker publishes it. */
function approval(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  return {
    version: 'v1',
    event: 'approval.requested',
    sessionId: 's1',
    payload: {
      approvalId: 'approval_1',
      sessionId: 's1',
      agentId: 'agent_1',
      toolCallId: 'call_1',
      toolName,
      inputJson: JSON.stringify(input),
    },
  }
}

/** A stored question request, as the worker publishes it. */
const question: Record<string, unknown> = {
  version: 'v1',
  event: 'capability.requested',
  sessionId: 's1',
  payload: {
    requestId: 'capreq_1',
    targetClientId: 'leapmux-a',
    capabilityName: 'tool_executor.askQuestion',
    payload: {
      executor: 'askQuestion',
      args: [
        'Which color do you prefer?',
        ['Red', 'Blue'],
      ],
      context: { toolCallId: 'call_q' },
    },
  },
}

/** The prompts that the Worker reads from the requests above. */
const PERMISSION_PROMPT: ControlPrompt = {
  kind: 'permission',
  permission: { title: 'editor', options: [] },
}
const PLAN_PROMPT: ControlPrompt = { kind: 'plan' }
const QUESTION_PROMPT: ControlPrompt = {
  kind: 'question',
  questions: [
    {
      question: 'Which color do you prefer?',
      options: [
        { label: 'Red' },
        { label: 'Blue' },
      ],
    },
  ],
}

/** One saved row: the native request, the answer, and the prompt that the Worker recorded. */
function saved(request: Record<string, unknown>, response: Record<string, unknown> | undefined, prompt?: ControlPrompt): PersistedControlResponse {
  return {
    requestId: 'r1',
    claimToken: '',
    request,
    response,
    ...(prompt
      ? { prompt }
      : {}),
  }
}

describe('cline plugin', () => {
  // A registration that is present but holds the readers of another provider passes a
  // presence check, so the test compares each hook with Cline's own function.
  it('registers the Cline readers for the CLINE provider', () => {
    expect(Object.keys(plugin.transcript).sort()).toEqual([
      'classify',
      'compactionStart',
      'extractDivider',
      'extractRow',
      'needsRequest',
      'notificationEntry',
      'spanRole',
    ])
    expect(plugin.transcript.compactionStart).toBe(clineCompactionStart)
    expect(plugin.transcript.classify).toBe(classifyClineMessage)
    expect(plugin.transcript.extractRow).toBe(clineExtractRow)
    expect(plugin.transcript.extractDivider).toBe(clineResultDivider)
    expect(plugin.transcript.notificationEntry).toBe(clineNotificationEntry)
    expect(plugin.transcript.spanRole).toBe(clineSpanRole)
    expect(plugin.transcript.needsRequest).toBe(clineNeedsRequest)
    expect(plugin.session?.compactionBoundaryFromMessage).toBe(clineCompactionBoundary)
    expect(plugin.controls).toBe(clineControls)
    expect(plugin.configuration).toBe(clineConfiguration)
  })

  // The worker's ValidateAttachment refuses a PDF and a binary file with the same policy.
  it('accepts text and images, and no PDF or binary', () => {
    expect(plugin.configuration?.attachments).toEqual({
      text: true,
      image: true,
      pdf: false,
      audio: false,
      binary: false,
    })
  })

  it('carries Plan on the permission-mode axis, as the plan toggle', () => {
    expect(plugin.configuration?.triggerModeGroupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode?.groupKey).toBe('permissionMode')
    expect(plugin.configuration?.planMode?.planValue).toBe(CLINE_PERMISSION_MODE.Plan)
    expect(plugin.configuration?.planMode?.defaultValue).toBe(CLINE_PERMISSION_MODE.Act)
    expect(plugin.configuration?.planMode?.currentMode({
      optionValues: { permissionMode: CLINE_PERMISSION_MODE.Plan },
    })).toBe(CLINE_PERMISSION_MODE.Plan)
  })

  it('states no child input', () => {
    expect(plugin.configuration?.supportsSubagentSend).toBeUndefined()
  })
})

describe('cline controls', () => {
  // The Worker reads each request and builds each answer, so the browser keeps only the
  // display of a saved answer and the presets.
  it('registers only the saved-answer display and the presets', () => {
    expect(Object.keys(controls).sort()).toEqual(['controlResponseDisplay', 'permissionPresets'])
  })

  it('offers the Bypass preset as Auto-approve and no Smart preset', () => {
    expect(controls.permissionPresets).toEqual({
      bypass: {
        sets: { permissionMode: CLINE_PERMISSION_MODE.AutoApprove },
      },
    })
  })
})

describe('cline saved answers', () => {
  const display = controls.controlResponseDisplay!

  it('words an approval and a refusal', () => {
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: true }, PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Allow' })
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: false, reason: 'Not that one.' }, PERMISSION_PROMPT))).toEqual({ kind: 'feedback', message: 'Not that one.' })
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: false, reason: CLINE_DECLINE_REASON.Tool }, PERMISSION_PROMPT))).toEqual({ kind: 'label', text: 'Deny' })
  })

  it('words the plan approval with the mode it switched to', () => {
    const plan = approval('switch_to_act_mode', {})
    expect(display(saved(plan, { approvalId: 'approval_1', approved: true, permissionMode: CLINE_PERMISSION_MODE.AutoApprove }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve (Auto-approve)' })
    expect(display(saved(plan, { approvalId: 'approval_1', approved: true }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(display(saved(plan, { approvalId: 'approval_1', approved: false, reason: 'Split it.' }, PLAN_PROMPT))).toEqual({ kind: 'feedback', message: 'Split it.' })
  })

  it('words a question\'s answer and its refusal', () => {
    expect(display(saved(question, {
      requestId: 'capreq_1',
      ok: true,
      payload: { result: 'Blue' },
    }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Blue' })
    expect(display(saved(question, { requestId: 'capreq_1', ok: false, error: CLINE_DECLINE_REASON.Question }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Declined' })
    expect(display(saved(question, { requestId: 'capreq_1', ok: false, error: 'Later.' }, QUESTION_PROMPT))).toEqual({ kind: 'feedback', message: 'Later.' })
  })

  it('reads nothing it does not know', () => {
    expect(display(saved(approval('editor', {}), undefined, PERMISSION_PROMPT))).toBeNull()
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1' }, PERMISSION_PROMPT))).toBeNull()
    expect(display(saved(question, { requestId: 'capreq_1' }, QUESTION_PROMPT))).toBeNull()
    expect(display(saved({ type: 'other' }, { approved: true }))).toBeNull()
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: true }))).toBeNull()
  })

  // The recorded prompt decides which reply the row holds and which words it shows,
  // not the native request beside it.
  it('reads the kind of the request from the recorded prompt', () => {
    expect(display(saved(approval('editor', {}), { approvalId: 'approval_1', approved: true }, PLAN_PROMPT))).toEqual({ kind: 'label', text: 'Approve' })
    expect(display(saved(approval('editor', {}), {
      requestId: 'capreq_1',
      ok: true,
      payload: { result: 'Blue' },
    }, QUESTION_PROMPT))).toEqual({ kind: 'label', text: 'Blue' })
  })
})

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      version: 'v1',
      event: 'tool.finished',
      sessionId: 'native-session',
      payload: {
        toolCallId: 'native-call',
        toolName: 'run_commands',
        output: [
          {
            query: 'printf preview',
            result: 'native inline preview',
            success: true,
          },
        ],
      },
    }
    const call = providerToolCall(AgentProvider.CLINE, frame, {
      spanId: 'native-call',
      spanType: 'run_commands',
      agentSessionId: 'native-session',
      role: 'result',
    })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.CLINE)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})
