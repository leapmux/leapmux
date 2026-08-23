import { describe, expect, it } from 'vitest'
import { classifyStartupUrl, STARTUP_BUCKETS } from './startupAssetBuckets'

/**
 * The LTE startup tracer ranks critical-path bytes by URL bucket. A wrong
 * classification silently moves weight between "fonts" and "critical_modules"
 * and would reorder the fix plan, so the pure classifier is guarded here.
 */
describe('classifyStartupUrl', () => {
  it('classifies fonts, entry, route, workers, rpc, and modules', () => {
    expect(classifyStartupUrl('http://localhost/fonts/HackNerdFont-3.003-Regular.woff2')).toBe('fonts')
    expect(classifyStartupUrl('http://localhost/_build/assets/client-CNz4oySH.js')).toBe('entry_js')
    expect(classifyStartupUrl('http://localhost/_build/assets/rolldown-runtime-hePW80VL.js')).toBe('entry_js')
    expect(classifyStartupUrl('http://localhost/_build/assets/(app)-BltZ6tvo.js')).toBe('route_app')
    expect(classifyStartupUrl('http://localhost/_build/assets/%28app%29-BltZ6tvo.js')).toBe('route_app')
    expect(classifyStartupUrl('http://localhost/_build/assets/shikiWorker-DUdJhQep.js')).toBe('workers')
    expect(classifyStartupUrl('http://localhost/_build/assets/markdownWorker-ByKVGkjx.js')).toBe('workers')
    expect(classifyStartupUrl('http://localhost/_build/assets/wasm-BnjxR4X6.js')).toBe('workers')
    expect(classifyStartupUrl('http://localhost/leapmux.v1.AuthService/GetSystemInfo')).toBe('rpc')
    expect(classifyStartupUrl('http://localhost/_build/assets/AuthContext-YaQM1q0h.js')).toBe('critical_modules')
    expect(classifyStartupUrl('http://localhost/_build/assets/shikiWorkerClient-BPYyHIt_.js')).toBe('critical_modules')
    expect(classifyStartupUrl('http://localhost/_build/assets/client-BQG5vqNI.css')).toBe('other')
  })

  it('exposes the seven buckets the report requires', () => {
    expect(STARTUP_BUCKETS).toEqual([
      'fonts',
      'entry_js',
      'critical_modules',
      'route_app',
      'workers',
      'rpc',
      'other',
    ])
  })
})
