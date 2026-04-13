import { afterEach, describe, expect, it, vi } from 'vitest'

import { executeCommand, getAllCommands, getCommand, registerCommand, resetCommands, unregisterCommand } from './commands'

afterEach(() => {
  resetCommands()
})

describe('command registry', () => {
  it('registers and retrieves a command', () => {
    registerCommand({ id: 'test.cmd', title: 'Test', handler: () => {} })
    const cmd = getCommand('test.cmd')
    expect(cmd).toBeDefined()
    expect(cmd!.id).toBe('test.cmd')
    expect(cmd!.title).toBe('Test')
  })

  it('returns undefined for unregistered commands', () => {
    expect(getCommand('no.such')).toBeUndefined()
  })

  it('executes a command handler', () => {
    const handler = vi.fn()
    registerCommand({ id: 'test.cmd', title: 'Test', handler })
    executeCommand('test.cmd')
    expect(handler).toHaveBeenCalledOnce()
  })

  it('executes unknown command gracefully', () => {
    // Should not throw
    executeCommand('no.such')
  })

  it('unregisters a command', () => {
    registerCommand({ id: 'test.cmd', title: 'Test', handler: () => {} })
    unregisterCommand('test.cmd')
    expect(getCommand('test.cmd')).toBeUndefined()
  })

  it('returns unregister function from registerCommand', () => {
    const unregister = registerCommand({ id: 'test.cmd', title: 'Test', handler: () => {} })
    expect(getCommand('test.cmd')).toBeDefined()
    unregister()
    expect(getCommand('test.cmd')).toBeUndefined()
  })

  it('re-registering same ID replaces handler', () => {
    const handler1 = vi.fn()
    const handler2 = vi.fn()
    registerCommand({ id: 'test.cmd', title: 'Test 1', handler: handler1 })
    registerCommand({ id: 'test.cmd', title: 'Test 2', handler: handler2 })
    executeCommand('test.cmd')
    expect(handler1).not.toHaveBeenCalled()
    expect(handler2).toHaveBeenCalledOnce()
  })

  it('unregister function only removes its own registration', () => {
    const unregister1 = registerCommand({ id: 'test.cmd', title: 'Test 1', handler: () => {} })
    registerCommand({ id: 'test.cmd', title: 'Test 2', handler: () => {} })
    // unregister1 should NOT remove the second registration
    unregister1()
    expect(getCommand('test.cmd')).toBeDefined()
    expect(getCommand('test.cmd')!.title).toBe('Test 2')
  })

  it('getAllCommands returns all registered commands', () => {
    registerCommand({ id: 'a', title: 'A', handler: () => {} })
    registerCommand({ id: 'b', title: 'B', handler: () => {} })
    registerCommand({ id: 'c', title: 'C', handler: () => {} })
    const all = getAllCommands()
    expect(all).toHaveLength(3)
    expect(all.map(c => c.id).sort()).toEqual(['a', 'b', 'c'])
  })

  it('handles async command errors gracefully', async () => {
    registerCommand({
      id: 'test.async',
      title: 'Async',
      handler: () => Promise.reject(new Error('fail')),
    })
    // Should not throw
    executeCommand('test.async')
  })
})
