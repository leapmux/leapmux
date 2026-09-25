import { describe, expect, it, vi } from 'vitest'
import { KIMI_DISPLAY, KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { kimiApprovalRequest, kimiQuestionRequest } from '~/test-support/kimiFixtures'
import { KIMI_OPTION, kimiApprovalDisplayKind, kimiExtractControl, kimiIsApproval, kimiPlanChoices, sendKimiPermissionOption } from './extractControl'

describe('kimiExtractControl', () => {
  it('reads a command approval as a permission with the three answers', () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'rm -rf build', cwd: '/work', language: 'bash' })
    const control = kimiExtractControl({ payload })
    expect(control).toStrictEqual({
      kind: 'permission',
      permission: {
        title: 'Bash',
        reason: 'Running: Bash',
        command: 'rm -rf build',
        workingDirectory: '/work',
        input: { kind: 'command', command: 'rm -rf build', cwd: '/work', language: 'bash' },
        options: [
          { optionId: 'approve', kind: 'allow_once', name: 'Allow' },
          { optionId: 'approve_for_session', kind: 'allow_always', name: 'Allow for this session' },
          { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
        ],
      },
    })
  })

  it('reads a plan review as the plan approval with its choices', () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, {
      kind: KIMI_DISPLAY.PlanReview,
      plan: '# Plan\n- A\n- B',
      options: [{ label: 'Option A', description: 'Simple' }, { label: 'Option B' }, { description: 'no label' }],
    })
    expect(kimiExtractControl({ payload })).toStrictEqual({
      kind: 'plan',
      text: '# Plan\n- A\n- B',
      choices: [
        { id: 'Option A', label: 'Approve: Option A', description: 'Simple', approves: true },
        { id: 'Option B', label: 'Approve: Option B', approves: true },
        { id: 'Revise', label: 'Request revisions', approves: false },
        { id: 'Reject and Exit', label: 'Reject and exit plan mode', approves: false },
      ],
    })
  })

  it('reads a plan review with no plan text and no options', () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, { kind: KIMI_DISPLAY.PlanReview, plan: ' ' })
    expect(kimiExtractControl({ payload })).toStrictEqual({ kind: 'plan', choices: kimiPlanChoices(undefined) })
  })

  // A subagent's plan review is the subagent's own. The plan approval would leave the
  // MAIN agent's plan mode, offer to clear the main context, and switch the main
  // permission mode, so the review reads as a permission with the plan's answers.
  it('reads a subagent plan review as a permission with the plan and its answers', () => {
    const display = {
      kind: KIMI_DISPLAY.PlanReview,
      plan: '# A subagent plan',
      options: [{ label: 'Option A', description: 'Simple' }, { label: 'Option B' }],
    }
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, display, { agent_id: 'agent-0', agentId: 'agent-0' })
    expect(kimiExtractControl({ payload })).toStrictEqual({
      kind: 'permission',
      permission: {
        title: 'ExitPlanMode',
        reason: 'Running: ExitPlanMode',
        // The plan reads as text, and the arguments keep what the text does not state.
        text: '# A subagent plan',
        input: { kind: KIMI_DISPLAY.PlanReview, options: display.options },
        options: [
          { optionId: 'approve', kind: 'allow_once', name: 'Approve the plan' },
          { optionId: 'plan_approve:Option A', kind: 'allow_once', name: 'Approve: Option A' },
          { optionId: 'plan_approve:Option B', kind: 'allow_once', name: 'Approve: Option B' },
          { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
          { optionId: 'plan_reject:Revise', kind: 'reject_once', name: 'Request revisions' },
          { optionId: 'plan_reject:Reject and Exit', kind: 'reject_once', name: 'Reject and exit plan mode' },
        ],
      },
    })
  })

  it('reads the envelope agent when the approval states no agent of its own', () => {
    const display = { kind: KIMI_DISPLAY.PlanReview, plan: '# Plan' }
    const subagent = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, display, { agent_id: '', agentId: 'agent-1' })
    expect(kimiExtractControl({ payload: subagent })?.kind).toBe('permission')
    const main = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, display, { agent_id: '', agentId: '' })
    expect(kimiExtractControl({ payload: main })?.kind).toBe('plan')
  })

  it('reads a goal start as a permission whose options pick the mode', () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.CreateGoal, { kind: KIMI_DISPLAY.GoalStart, objective: 'Ship it', mode: 'manual' })
    const control = kimiExtractControl({ payload })
    expect(control?.kind).toBe('permission')
    if (control?.kind !== 'permission')
      return
    expect(control.permission.options).toStrictEqual([
      { optionId: 'approve', kind: 'allow_once', name: 'Start the goal' },
      { optionId: 'goal_mode:manual', kind: 'allow_once', name: 'Start in Always Ask' },
      { optionId: 'goal_mode:yolo', kind: 'allow_once', name: 'Start in Ask When Needed' },
      { optionId: 'goal_mode:auto', kind: 'allow_once', name: 'Start in Never Ask' },
      { optionId: 'reject', kind: 'reject_once', name: 'Decline' },
    ])
  })

  // The server words every approval through its display, and a bare one states only
  // the tool it asks about.
  it('reads an approval that states no display and no action as a bare permission', () => {
    const { tool_input_display: _display, ...payload } = kimiApprovalRequest(KIMI_TOOL.Write, {}, { action: '' })
    expect(kimiExtractControl({ payload })).toStrictEqual({
      kind: 'permission',
      permission: {
        title: 'Write',
        options: [
          { optionId: 'approve', kind: 'allow_once', name: 'Allow' },
          { optionId: 'approve_for_session', kind: 'allow_always', name: 'Allow for this session' },
          { optionId: 'reject', kind: 'reject_once', name: 'Deny' },
        ],
      },
    })
    expect(kimiApprovalDisplayKind(payload)).toBe('')
  })

  it('keeps the whole display of a subagent plan review that states no plan text', () => {
    const display = { kind: KIMI_DISPLAY.PlanReview, plan: '  ', options: [{ label: 'Option A' }] }
    const payload = kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, display, { agent_id: 'agent-0' })
    const control = kimiExtractControl({ payload })
    expect(control?.kind).toBe('permission')
    if (control?.kind !== 'permission')
      return
    expect(control.permission.text).toBeUndefined()
    expect(control.permission.input).toStrictEqual(display)
  })

  it('offers only the two plan refusals for a plan that lists no approach', () => {
    expect(kimiPlanChoices({ options: 'not a list' })).toStrictEqual([
      { id: 'Revise', label: 'Request revisions', approves: false },
      { id: 'Reject and Exit', label: 'Reject and exit plan mode', approves: false },
    ])
  })

  it('reads no control from a question or another row', () => {
    expect(kimiExtractControl({ payload: kimiQuestionRequest([]) })).toBeNull()
    expect(kimiExtractControl({ payload: {} })).toBeNull()
  })

  it('recognizes an approval and its display kind', () => {
    const payload = kimiApprovalRequest(KIMI_TOOL.Write, { kind: 'file_io' })
    expect(kimiIsApproval(payload)).toBe(true)
    expect(kimiApprovalDisplayKind(payload)).toBe('file_io')
    expect(kimiIsApproval(kimiQuestionRequest([]))).toBe(false)
  })
})

describe('sendKimiPermissionOption', () => {
  async function sent(optionId: string) {
    const onRespond = vi.fn().mockResolvedValue(undefined)
    await sendKimiPermissionOption(onRespond, 'approval_1', optionId)
    const [bytes] = onRespond.mock.calls[0] ?? []
    return JSON.parse(new TextDecoder().decode(bytes)).response.response
  }

  it('sends each answer in the neutral envelope', async () => {
    expect(await sent(KIMI_OPTION.Approve)).toStrictEqual({ behavior: 'allow' })
    expect(await sent(KIMI_OPTION.ApproveForSession)).toStrictEqual({ behavior: 'allow', scope: 'session' })
    expect(await sent(KIMI_OPTION.Reject)).toStrictEqual({ behavior: 'deny', message: 'Rejected by user.' })
    expect(await sent('goal_mode:auto')).toStrictEqual({ behavior: 'allow', choice: 'auto' })
  })

  it('sends the plan label an answer to a subagent plan carries', async () => {
    expect(await sent('plan_approve:Option B')).toStrictEqual({ behavior: 'allow', choice: 'Option B' })
    expect(await sent('plan_reject:Revise')).toStrictEqual({ behavior: 'deny', message: 'Rejected by user.', choice: 'Revise' })
    expect(await sent('plan_reject:Reject and Exit')).toStrictEqual({ behavior: 'deny', message: 'Rejected by user.', choice: 'Reject and Exit' })
  })

  it('sends every option each Kimi control offers', async () => {
    const offered = [
      kimiApprovalRequest(KIMI_TOOL.Bash, { kind: 'command', command: 'ls' }),
      kimiApprovalRequest(KIMI_TOOL.CreateGoal, { kind: KIMI_DISPLAY.GoalStart, objective: 'Ship it' }),
      kimiApprovalRequest(KIMI_TOOL.ExitPlanMode, { kind: KIMI_DISPLAY.PlanReview, plan: '# P', options: [{ label: 'Option A' }] }, { agent_id: 'agent-0' }),
    ].flatMap((payload) => {
      const control = kimiExtractControl({ payload })
      return control?.kind === 'permission' ? control.permission.options : []
    })
    // Three permission answers, five goal answers, and five subagent plan answers.
    expect(offered).toHaveLength(13)
    for (const option of offered)
      expect((await sent(option.optionId)).behavior, option.optionId).toBe(option.kind.startsWith('allow') ? 'allow' : 'deny')
  })

  // The option ids are LeapMux's own, so an id no Kimi control offers is a defect in
  // the caller. It must never read as an approval.
  it('refuses an option id that no Kimi control offers', async () => {
    for (const optionId of ['', 'rejec', 'allow', 'approve_once'])
      expect(await sent(optionId), optionId).toStrictEqual({ behavior: 'deny', message: 'Rejected by user.' })
  })
})
