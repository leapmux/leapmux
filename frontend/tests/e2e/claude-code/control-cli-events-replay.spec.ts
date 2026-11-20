import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { mintCLITokenForAdmin, runCLI, startControlEventStream } from '../helpers/cli'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { chooseSettingsOption, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

claudeTest('replays a permission requested and resolved while the event client was disconnected', async ({ native, leapmuxServer }) => {
  const agentId = await selectedAgentTabId(native.page)
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const cli = await mintCLITokenForAdmin(leapmuxServer)
  const dir = createTestDirectory('leapmux-agent-event-')
  const checkpoint = join(dir, 'checkpoint.json')
  const args = [
    '--tab',
    agentId,
    '--checkpoint',
    checkpoint,
    '--kind',
    'stream.ready,agent.request,agent.request_ended',
  ]

  const first = startControlEventStream(cli, args)
  try {
    await expect.poll(() => first.events().map(event => event.kind)).toContain('stream.ready')
    await expect.poll(() => existsSync(checkpoint)).toBe(true)
  }
  finally {
    first.child.kill('SIGINT')
    await first.finished
  }
  if (process.platform !== 'win32')
    expect(await first.finished, first.diagnostic()).toEqual({ code: 0, signal: null })

  const probe = join(dir, 'permission-probe.txt')
  const command = `printf permitted > ${quotePosixShellArgument(probe)}; cat ${quotePosixShellArgument(probe)}`
  const step = await native.modelScript.queue({
    toolCalls: [
      bashToolCall(native.provider, 'event-replay-permission', command),
    ],
  }, { text: 'The permission probe is complete.' })
  await runCLI(cli, [
    'agent',
    'send',
    agentId,
    native.modelScript.prompt('Run the scripted permission probe.'),
  ])
  await waitForControlBanner(native.page)
  const { requests } = await runCLI(cli, ['agent', 'requests', agentId]) as { requests: Array<{ id: string }> }
  expect(requests).toHaveLength(1)
  const [request] = requests
  if (!request)
    throw new Error('The Worker returned no permission request.')
  await runCLI(cli, [
    'agent',
    'approve',
    agentId,
    request.id,
  ])
  await native.modelScript.waitForSteps(step + 2)
  await waitForAgentIdle(native.page)
  expect(readFileSync(probe, 'utf8')).toBe('permitted')

  const resumed = startControlEventStream(cli, [...args, '--limit', '2'])
  try {
    await expect.poll(() => resumed.events().map(event => event.kind)).toContain('agent.request_ended')
    expect(await resumed.finished, resumed.diagnostic()).toEqual({ code: 0, signal: null })
    const facts = resumed.events().filter(event => event.kind === 'agent.request' || event.kind === 'agent.request_ended')
    expect(facts).toEqual([
      expect.objectContaining({ kind: 'agent.request', prompt: 'permission', replayed: true }),
      expect.objectContaining({ kind: 'agent.request_ended', replayed: true }),
    ])
    const [opened, ended] = facts
    expect(opened?.request_instance).toEqual(expect.any(String))
    expect(ended?.request_instance).toBe(opened?.request_instance)
    expect(opened?.request).toBeUndefined()
    expect(ended?.request).toBeUndefined()
    expect(resumed.diagnostic()).not.toContain(request.id)
  }
  finally {
    resumed.child.kill()
    await resumed.finished
  }
})
