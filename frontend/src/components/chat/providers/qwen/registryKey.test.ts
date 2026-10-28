import type { QwenRegistrySource } from './registryKey'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { QWEN_TOOL } from '~/generated/contracts/qwen-protocol'
import { qwenRegistryRowKey } from './registryKey'

interface RegistryKeyVector {
  name: string
  source: QwenRegistrySource
  sessionId: string
  toolCallId: string
  dispatchId: string
  expectedKey: string
}

// Vite rewrites new URL(relative, import.meta.url) into an HTTP asset URL in
// the DOM project. Resolve the fixture path from the unchanged file URL instead.
const fixturePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../testdata/qwen_registry_key_conformance.json')
const fixture = JSON.parse(readFileSync(fixturePath, 'utf8')) as {
  cases: RegistryKeyVector[]
}

describe('qwenRegistryRowKey', () => {
  it.each(fixture.cases)('matches the shared vector $name', (vector) => {
    const key = qwenRegistryRowKey(vector.source, vector.sessionId, vector.toolCallId, vector.dispatchId)
    expect(key ?? '').toBe(vector.expectedKey)
    if (key !== undefined) {
      expect(key).toHaveLength(81)
      expect(key).toMatch(/^qwen-registry-v1:[0-9a-f]{64}$/)
    }
  })

  it('omits a key when the stored owner is absent', () => {
    expect(qwenRegistryRowKey(QWEN_TOOL.Agent, undefined, 'call')).toBeUndefined()
    expect(qwenRegistryRowKey(QWEN_TOOL.Agent, '', 'call')).toBeUndefined()
    expect(qwenRegistryRowKey(QWEN_TOOL.Agent, 'owner', '')).toBeUndefined()
  })

  it('keeps large identities distinct without increasing the key length', () => {
    const large = 'opaque:'.repeat(1 << 15)
    const first = qwenRegistryRowKey(QWEN_TOOL.Agent, large, `${large}first`)
    const second = qwenRegistryRowKey(QWEN_TOOL.Agent, large, `${large}second`)
    expect(first).toHaveLength(81)
    expect(second).toHaveLength(81)
    expect(first).not.toBe(second)
    expect(first).toBe(qwenRegistryRowKey(QWEN_TOOL.Agent, large, `${large}first`))
  })
})
