import { runInNewContext } from 'node:vm'
import { describe, expect, it } from 'vitest'
import { nativeWorkflowScripts } from './nativeWorkflowScripts'

describe('nativeWorkflowScripts', () => {
  it('executes a computed result and a distinct computed error', () => {
    const scripts = nativeWorkflowScripts('PROBE123')
    expect(scripts).toHaveLength(2)
    expect(scripts.map(script => script.failed)).toEqual([false, true])
    expect(new Set(scripts.map(script => script.callId)).size).toBe(2)
    for (const script of scripts) {
      expect(script.source).not.toContain(script.expected)
      expect(script.name).toMatch(/^[a-z0-9]+(?:-[a-z0-9]+)*$/)
      const run = () => runInNewContext(`(function () { ${script.source.replace('export const meta', 'const meta')} })()`)
      if (script.failed)
        expect(run).toThrow(script.expected)
      else
        expect(run()).toBe(script.expected)
    }
  })

  it.each(['', ' ', 'a-b', '界', 'a"b', 'a\nb'])('rejects an invalid marker: %j', (marker) => {
    expect(() => nativeWorkflowScripts(marker)).toThrow('letters and digits')
  })
})
