import type { TestInfo } from '@playwright/test'
import { existsSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/** Attach the isolated Copilot runtime's native event and process logs. */
export async function attachCopilotNativeArtifacts(home: string | undefined, testInfo: TestInfo): Promise<void> {
  if (!home) {
    await testInfo.attach('copilot-native-home-missing', { body: 'The isolated COPILOT_HOME is absent.', contentType: 'text/plain' })
    return
  }
  const sessions = join(home, 'session-state')
  if (existsSync(sessions)) {
    for (const entry of readdirSync(sessions, { withFileTypes: true })) {
      const path = join(sessions, entry.name, 'events.jsonl')
      if (entry.isDirectory() && existsSync(path))
        await testInfo.attach(`copilot-${entry.name}-events`, { path, contentType: 'application/x-ndjson' })
    }
  }
  const logs = join(home, 'logs')
  if (existsSync(logs)) {
    for (const entry of readdirSync(logs, { withFileTypes: true })) {
      if (entry.isFile() && entry.name.endsWith('.log'))
        await testInfo.attach(`copilot-${entry.name}`, { path: join(logs, entry.name), contentType: 'text/plain' })
    }
  }
}
