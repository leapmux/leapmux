/**
 * End-to-end coverage for Claude-compatible `agent` CLI commands and
 * `tab open-image` against a live Hub, Worker, real Claude Code process, and
 * the scripted mock model server:
 *
 *   agent providers|sessions|list|get|open|request-status|rename|reveal|set,
 *   agent usage|todos|tasks|tree|open-tab|messages|message|span|marks|export|search,
 *   agent fork|compact|clear|close|startup approve|startup cancel,
 *   agent send|wait|requests|approve|deny|answer|interrupt,
 *   agent goal get|set|pause|resume|clear,
 *   agent queue list|get|edit|move|remove|clear|pause|resume|steer|preempt|advance|retry,
 *   tab open-image.
 */

import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { CLIError, mintCLITokenForAdmin, runCLI } from '../helpers/cli'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { askUserQuestionToolCall, bashToolCall, readToolCall, spawnSubagentToolCall, updateTodosToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { runToolImageTurn } from '../helpers/toolImages'
import { chooseSettingsOption, tabById, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

claudeTest.describe('control CLI agents full coverage', () => {
  claudeTest('exercises discovery, lifecycle, todos, subagents, transcripts, fork, export, search, and image tabs', async ({
    native,
    authenticatedClaudeWorkspace,
    leapmuxServer,
  }) => {
    const { workspaceId, workingDir } = authenticatedClaudeWorkspace
    const agentId = (await currentNativeAgent(native)).id
    const { page, modelScript } = native
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
    await waitForSettingsIdle(page)

    // 1. `agent providers`, `agent list`, `agent get`, `agent sessions`.
    const providers = await runCLI(cli, [
      'agent',
      'providers',
      '--worker',
      leapmuxServer.workerId,
    ]) as { providers: Array<{ name: string, aliases: string[] }> }
    expect(providers.providers.some(p => p.aliases.includes('claude'))).toBe(true)

    const listed = await runCLI(cli, [
      'agent',
      'list',
      '--workspace',
      workspaceId,
    ]) as {
      agents: Array<{ id: string }>
    }
    expect(listed.agents.some(a => a.id === agentId)).toBe(true)

    const got = await runCLI(cli, ['agent', 'get', agentId, '--options']) as { id: string }
    expect(got.id).toBe(agentId)

    // 2. `agent open` with `--message --wait --request-id` + `agent request-status`.
    const uniqueToken = `UNIQUE_CLI_SEARCH_TOKEN_${Date.now()}`
    const openReqId = `cli-open-req-${Date.now()}`
    const openStart = await modelScript.queue({
      text: `Opened agent reporting ${uniqueToken}.`,
    })
    const opened = await runCLI(cli, [
      'agent',
      'open',
      '--workspace',
      workspaceId,
      '--worker',
      leapmuxServer.workerId,
      '--provider',
      'claude',
      '--dir',
      workingDir,
      '--name',
      'cli-opened',
      '--message',
      modelScript.prompt('Report the search marker token.'),
      '--request-id',
      openReqId,
      '--wait',
      '--timeout',
      '30s',
    ]) as { id: string, reply?: string }
    await modelScript.waitForSteps(openStart + 1)
    expect(opened.id).toBeTruthy()

    const reqStatus = await runCLI(cli, [
      'agent',
      'request-status',
      opened.id,
      openReqId,
    ]) as { status: string, agent_id: string }
    expect(reqStatus.agent_id).toBe(opened.id)
    expect(reqStatus.status).toBe('completed')

    const sessions = await runCLI(cli, [
      'agent',
      'sessions',
      '--provider',
      'claude',
      '--worker',
      leapmuxServer.workerId,
      '--dir',
      workingDir,
    ]) as { sessions: unknown[] }
    expect(Array.isArray(sessions.sessions)).toBe(true)

    // 3. `agent rename`, `agent reveal`, `agent set`, `agent usage`.
    const renamed = await runCLI(cli, [
      'agent',
      'rename',
      opened.id,
      '--name',
      'cli-renamed',
    ]) as { id: string, title?: string, name?: string }
    expect(renamed.id).toBe(opened.id)

    await runCLI(cli, ['agent', 'reveal', opened.id])
    await runCLI(cli, [
      'agent',
      'set',
      opened.id,
      '--permission-mode',
      'bypassPermissions',
    ])

    const usage = await runCLI(cli, ['agent', 'usage', opened.id]) as { agent_id: string }
    expect(usage.agent_id).toBe(opened.id)

    // 4. Real todos -> `agent todos`.
    const todoStart = await modelScript.queue(
      {
        toolCalls: [
          updateTodosToolCall(AgentProvider.CLAUDE_CODE, 'cli-todos-1', [
            { step: 'Inspect CLI surface', status: 'completed' },
            { step: 'Verify todo output', status: 'in_progress' },
          ]),
        ],
      },
      { text: 'Updated the todo list.' },
    )
    await runCLI(cli, [
      'agent',
      'send',
      opened.id,
      modelScript.prompt('Write two todos.'),
      '--wait',
      '--timeout',
      '30s',
    ])
    await modelScript.waitForSteps(todoStart + 2)

    const todos = await runCLI(cli, ['agent', 'todos', opened.id]) as {
      todos: Array<{ content: string, status: string }>
    }
    expect(todos.todos.some(t => t.content === 'Inspect CLI surface')).toBe(true)

    // 5. Real subagent spawn -> `agent tasks`, `agent tree`, `agent open-tab`, `agent span`.
    const subSpanId = 'cli-spawn-sub-1'
    await modelScript.fallback({ text: 'Subagent finished writing summary.' })
    const subStart = await modelScript.queue({
      toolCalls: [
        spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, subSpanId, {
          description: 'CLI subagent summary',
          prompt: modelScript.prompt('Write one short sentence.'),
        }),
      ],
    })
    await runCLI(cli, [
      'agent',
      'send',
      opened.id,
      modelScript.prompt('Spawn a subagent for CLI tree test.'),
      '--wait',
      '--timeout',
      '30s',
    ])
    await modelScript.waitForSteps(subStart + 1)

    let subagentId = ''
    await expect.poll(async () => {
      const tasks = await runCLI(cli, ['agent', 'tasks', opened.id]) as {
        tasks: Array<{ id: string, child_agent_id?: string }>
      }
      const child = tasks.tasks.find(t => t.child_agent_id)
      subagentId = child?.child_agent_id ?? ''
      return subagentId
    }).not.toBe('')

    const tree = await runCLI(cli, ['agent', 'tree', opened.id]) as {
      root: { id: string, children?: Array<{ id: string }> }
    }
    expect(tree.root.id).toBe(opened.id)
    expect((tree.root.children ?? []).some(c => c.id === subagentId)).toBe(true)

    const openedSubTab = await runCLI(cli, [
      'agent',
      'open-tab',
      subagentId,
      '--workspace',
      workspaceId,
    ]) as { id: string }
    expect(openedSubTab.id).toBe(subagentId)

    const spanMsgs = await runCLI(cli, ['agent', 'span', opened.id, subSpanId]) as {
      messages: unknown[]
    }
    expect(Array.isArray(spanMsgs.messages)).toBe(true)

    // 6. Real transcript inspection (`messages`, `message`, `marks`, `export`, `search`).
    const msgs = await runCLI(cli, ['agent', 'messages', opened.id]) as {
      messages: Array<{ seq: number }>
    }
    expect(msgs.messages.length).toBeGreaterThan(0)
    const firstSeq = msgs.messages[0]!.seq

    const singleMsg = await runCLI(cli, [
      'agent',
      'message',
      opened.id,
      String(firstSeq),
    ]) as { seq: number }
    expect(singleMsg.seq).toBe(firstSeq)

    const marks = await runCLI(cli, ['agent', 'marks', opened.id]) as {
      agent_id: string
      marks: Array<{ seq: number, type: string }>
    }
    expect(marks.agent_id).toBe(opened.id)
    expect(marks.marks.length).toBeGreaterThan(0)

    const exportFile = join(createTestDirectory('leapmux-cli-export-'), 'transcript.md')
    const exported = await runCLI(cli, [
      'agent',
      'export',
      opened.id,
      '--style',
      'markdown',
      '--output',
      exportFile,
    ]) as { messages: number, output: string }
    expect(exported.messages).toBeGreaterThan(0)
    expect(existsSync(exportFile)).toBe(true)
    expect(readFileSync(exportFile, 'utf8')).toContain(uniqueToken)

    const search = await runCLI(cli, [
      'agent',
      'search',
      uniqueToken,
      '--agent',
      opened.id,
    ]) as { hits: Array<{ agent_id: string }> }
    expect(search.hits.some(h => h.agent_id === opened.id)).toBe(true)

    // 7. Real PNG tool result on primary agent -> `tab open-image`.
    await runCLI(cli, ['agent', 'reveal', agentId])
    await expect(tabById(page, agentId)).toHaveAttribute('aria-selected', 'true')
    await runToolImageTurn(native, {
      workingDir,
      marker: 'cli-open-image',
      toolCall: image => readToolCall(native.provider, 'cli-read-png', image.fileName),
    })
    const primaryMsgs = await runCLI(cli, ['agent', 'messages', agentId, '--raw']) as {
      messages: Array<{ seq: number, content?: unknown }>
    }
    const imageMsg = [...primaryMsgs.messages].reverse().find((m) => {
      const s = JSON.stringify(m)
      return s.includes('image/png') || s.includes('"type":"image"')
    })
    expect(imageMsg, `expected a transcript message containing the PNG tool result in ${JSON.stringify(primaryMsgs.messages)}`).toBeDefined()
    const openedImgTab = await runCLI(cli, [
      'tab',
      'open-image',
      agentId,
      String(imageMsg!.seq),
      '--title',
      'CLI Image Tab',
    ]) as { id: string }
    expect(openedImgTab.id).toBeTruthy()

    // 8. `agent fork`, `agent compact`, `agent clear`, `agent startup approve|cancel`, `agent close`.
    const forked = await runCLI(cli, [
      'agent',
      'fork',
      opened.id,
      '--fresh',
      '--name',
      'cli-forked',
    ]) as { id: string }
    expect(forked.id).toBeTruthy()
    expect(forked.id).not.toBe(opened.id)

    await runCLI(cli, ['agent', 'compact', opened.id])
    await runCLI(cli, ['agent', 'clear', opened.id])

    // Startup prompts only arise during OS Keychain confirmation; confirm both
    // CLI verbs reach the Worker's ListAgents RPC and report `not_found`.
    await expect(runCLI(cli, [
      'agent',
      'startup',
      'approve',
      opened.id,
    ])).rejects.toMatchObject({ code: 'not_found' })
    await expect(runCLI(cli, [
      'agent',
      'startup',
      'cancel',
      opened.id,
    ])).rejects.toMatchObject({ code: 'not_found' })

    await runCLI(cli, [
      'agent',
      'close',
      forked.id,
      '--allow-busy',
    ])
    await runCLI(cli, [
      'agent',
      'close',
      opened.id,
      '--allow-busy',
    ])
  })

  claudeTest('exercises permissions approve and deny, questions answer, wait, and interrupt', async ({
    native,
    leapmuxServer,
  }) => {
    const agentId = (await currentNativeAgent(native)).id
    const { page, modelScript } = native
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    await chooseSettingsOption(page, 'permissionMode-default')
    await waitForSettingsIdle(page)

    // 1. Permission `requests` + `deny` via CLI (protected file must NOT be created).
    const deniedFile = join(createTestDirectory('claude-cli-deny-'), 'denied.txt')
    const denyStart = await modelScript.queue(
      {
        toolCalls: [
          bashToolCall(native.provider, 'cli-perm-deny', `printf denied > ${quotePosixShellArgument(deniedFile)}`),
        ],
      },
      { text: 'Understood, command was denied.' },
    )
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Run the command that will be denied.'),
    ])
    await runCLI(cli, [
      'agent',
      'wait',
      agentId,
      '--until',
      'waiting_for_user',
      '--timeout',
      '30s',
    ])
    const denyReqs = await runCLI(cli, ['agent', 'requests', agentId]) as {
      requests: Array<{ id: string }>
    }
    expect(denyReqs.requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'deny',
      agentId,
      denyReqs.requests[0]!.id,
      '--message',
      'Denied by CLI E2E',
    ])
    await modelScript.waitForSteps(denyStart + 2)
    await runCLI(cli, [
      'agent',
      'wait',
      agentId,
      '--until',
      'idle',
      '--timeout',
      '30s',
    ])
    expect(existsSync(deniedFile)).toBe(false)

    // 2. Permission `requests` + `approve` via CLI (protected file IS created).
    const approvedFile = join(createTestDirectory('claude-cli-approve-'), 'approved.txt')
    const approveStart = await modelScript.queue(
      {
        toolCalls: [
          bashToolCall(native.provider, 'cli-perm-approve', `printf approved > ${quotePosixShellArgument(approvedFile)}`),
        ],
      },
      { text: 'Approved command succeeded.' },
    )
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Run the command that will be approved.'),
    ])
    await waitForControlBanner(page)
    const approveReqs = await runCLI(cli, ['agent', 'requests', agentId]) as {
      requests: Array<{ id: string }>
    }
    expect(approveReqs.requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'approve',
      agentId,
      approveReqs.requests[0]!.id,
    ])
    await modelScript.waitForSteps(approveStart + 2)
    await waitForAgentIdle(page)
    expect(readFileSync(approvedFile, 'utf8')).toBe('approved')

    // 3. Question `requests` + `answer --option` via CLI.
    const questionStart = await modelScript.queue(
      {
        toolCalls: [
          askUserQuestionToolCall(native.provider, 'cli-question-1', [
            {
              question: 'Pick a color',
              header: 'Color',
              options: [
                { label: 'Red', description: 'Red option' },
                { label: 'Blue', description: 'Blue option' },
              ],
            },
          ]),
        ],
      },
      { text: 'Blue selected.' },
    )
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Ask a color question.'),
    ])
    await waitForControlBanner(page)
    const qReqs = await runCLI(cli, ['agent', 'requests', agentId]) as {
      requests: Array<{ id: string }>
    }
    expect(qReqs.requests).toHaveLength(1)
    await runCLI(cli, [
      'agent',
      'answer',
      agentId,
      qReqs.requests[0]!.id,
      '--option',
      '2',
    ])
    await modelScript.waitForSteps(questionStart + 2)
    await waitForAgentIdle(page)

    // 4. `agent interrupt` stops an in-flight gated turn cleanly.
    const interruptGate = 'cli-interrupt-gate'
    await modelScript.queue({ gate: interruptGate, text: 'Never finishes normally.' })
    await withCleanup(async () => {
      await runCLI(cli, [
        'agent',
        'send',
        agentId,
        modelScript.prompt('Hold on the interrupt gate.'),
      ])
      await modelScript.waitForGate(interruptGate)
      await runCLI(cli, [
        'agent',
        'interrupt',
        agentId,
        '--reason',
        'CLI interrupt test',
      ])
      await modelScript.releaseGateIfHeld(interruptGate)
      await runCLI(cli, [
        'agent',
        'wait',
        agentId,
        '--until',
        'idle',
        '--timeout',
        '30s',
      ])
    }, async () => {
      await modelScript.releaseGateIfHeld(interruptGate)
    })
  })

  claudeTest('exercises goal commands and all twelve queue commands', async ({
    native,
    leapmuxServer,
  }) => {
    const agentId = (await currentNativeAgent(native)).id
    const { page, modelScript } = native
    const cli = await mintCLITokenForAdmin(leapmuxServer)

    await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
    await waitForSettingsIdle(page)

    // 1. Claude supports `agent goal get|set|clear` after its initial startup turn
    //    advertises `/goal` (pause/resume are exercised natively in the Codex spec).
    await modelScript.fallback({ text: 'Understood.' })
    await sendNativeAnswer(native, 'Reply ready to advertise goal commands.', 'ready')

    const initialGoal = await runCLI(cli, ['agent', 'goal', 'get', agentId]) as { agent_id: string }
    expect(initialGoal.agent_id).toBe(agentId)

    const goalSet = await runCLI(cli, [
      'agent',
      'goal',
      'set',
      agentId,
      modelScript.prompt('Keep all CLI E2E tests green.'),
    ]) as { id: string, action: string }
    expect(goalSet.id).toBe(agentId)

    await runCLI(cli, ['agent', 'goal', 'clear', agentId])
    await waitForAgentIdle(page)

    // 2. All 12 `agent queue` commands.
    // Pause the queue first so queued items stay unlocked for inspection, editing, moving, removing, and clearing.
    const paused = await runCLI(cli, ['agent', 'queue', 'pause', agentId]) as { paused: boolean }
    expect(paused.paused).toBe(true)

    const q1 = await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Queued item one.'),
    ]) as { input_id: string }
    const q2 = await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Queued item two.'),
    ]) as { input_id: string }
    const q3 = await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Queued item three.'),
    ]) as { input_id: string }

    const qList = await runCLI(cli, ['agent', 'queue', 'list', agentId]) as {
      paused: boolean
      items: Array<{ id: string }>
    }
    expect(qList.paused).toBe(true)
    expect(qList.items.map(i => i.id)).toEqual(expect.arrayContaining([q1.input_id, q2.input_id, q3.input_id]))

    const qGot = await runCLI(cli, [
      'agent',
      'queue',
      'get',
      agentId,
      q1.input_id,
    ]) as { id: string }
    expect(qGot.id).toBe(q1.input_id)

    await runCLI(cli, [
      'agent',
      'queue',
      'edit',
      agentId,
      q1.input_id,
      modelScript.prompt('Edited queued item one.'),
    ])

    await runCLI(cli, [
      'agent',
      'queue',
      'move',
      agentId,
      q2.input_id,
      '--first',
    ])

    await runCLI(cli, [
      'agent',
      'queue',
      'remove',
      agentId,
      q3.input_id,
    ])
    await runCLI(cli, ['agent', 'queue', 'clear', agentId])

    const afterClear = await runCLI(cli, ['agent', 'queue', 'list', agentId]) as {
      items: unknown[]
    }
    expect(afterClear.items).toHaveLength(0)

    // `agent queue retry` on an empty healthy queue must fail cleanly via the real Worker guard.
    await expect(runCLI(cli, ['agent', 'queue', 'retry', agentId])).rejects.toBeInstanceOf(CLIError)

    // `agent queue advance` resumes a paused queue with queued work.
    const advStart = await modelScript.queue({ text: 'Advanced queued reply.' })
    await runCLI(cli, [
      'agent',
      'send',
      agentId,
      modelScript.prompt('Advance this paused item.'),
    ])
    await runCLI(cli, ['agent', 'queue', 'advance', agentId])
    await modelScript.waitForSteps(advStart + 1)
    await waitForAgentIdle(page)

    // `agent queue resume` ensures resumed state idempotently.
    await runCLI(cli, ['agent', 'queue', 'resume', agentId])

    // `agent queue steer` during an active gated turn.
    const steerGate = 'cli-queue-steer-gate'
    const steerStart = await modelScript.queue(
      { gate: steerGate, text: 'Lead turn done.' },
      { text: 'Steered turn done.' },
    )
    await withCleanup(async () => {
      await runCLI(cli, [
        'agent',
        'send',
        agentId,
        modelScript.prompt('Lead turn for steer.'),
      ])
      await modelScript.waitForGate(steerGate)
      const steerItem = await runCLI(cli, [
        'agent',
        'send',
        agentId,
        modelScript.prompt('Steer into lead.'),
      ]) as {
        input_id: string
      }
      await runCLI(cli, [
        'agent',
        'queue',
        'steer',
        agentId,
        steerItem.input_id,
      ])
      await modelScript.releaseGate(steerGate)
      await modelScript.waitForSteps(steerStart + 2)
      await waitForAgentIdle(page)
    }, async () => {
      await modelScript.releaseGateIfHeld(steerGate)
    })

    // `agent queue preempt` during an active gated turn.
    // Claude Code supports in-turn steering rather than preemption; confirm the
    // real Worker refuses preemption with `conflict` and preserves the queued item.
    const preemptGate = 'cli-queue-preempt-gate'
    const preemptStart = await modelScript.queue(
      { gate: preemptGate, text: 'Lead turn finishes.' },
      { text: 'Queued follow-up finishes.' },
    )
    await withCleanup(async () => {
      await runCLI(cli, [
        'agent',
        'send',
        agentId,
        modelScript.prompt('Lead turn for preempt.'),
      ])
      await modelScript.waitForGate(preemptGate)
      const preemptItem = await runCLI(cli, [
        'agent',
        'send',
        agentId,
        modelScript.prompt('Preempt lead turn.'),
      ]) as {
        input_id: string
      }
      await expect(runCLI(cli, [
        'agent',
        'queue',
        'preempt',
        agentId,
        preemptItem.input_id,
      ])).rejects.toMatchObject({ code: 'conflict' })
      await modelScript.releaseGate(preemptGate)
      await modelScript.waitForSteps(preemptStart + 2)
      await waitForAgentIdle(page)
    }, async () => {
      await modelScript.releaseGateIfHeld(preemptGate)
    })
  })
})
