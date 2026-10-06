import { existsSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { currentNativeAgent } from '../helpers/nativeScenario'

claudeTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const configDir = environment.CLAUDE_CONFIG_DIR!
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, configDir],
    inlineConfiguration: [environment.ANTHROPIC_BASE_URL!, environment.ANTHROPIC_API_KEY!],
  })
  // Claude Code stores the session of the turn under `projects/` of its configuration directory, where the Worker
  // reads its stored sessions.
  const { agentSessionId } = await currentNativeAgent(native)
  expect(agentSessionId).not.toBe('')
  const projects = join(configDir, 'projects')
  const sessionFiles = () => existsSync(projects)
    ? readdirSync(projects, { withFileTypes: true })
        .filter(entry => entry.isDirectory())
        .map(entry => join(projects, entry.name, `${agentSessionId}.jsonl`))
        .filter(path => existsSync(path))
    : []
  await expect.poll(sessionFiles, { message: `Claude Code stores the session ${agentSessionId} under ${projects}` }).toHaveLength(1)
})
