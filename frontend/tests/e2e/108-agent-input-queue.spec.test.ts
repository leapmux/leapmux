import { describe, expect, it, vi } from 'vitest'

const registered = vi.hoisted(() => new Map<string, unknown>())
vi.mock('./process-control-fixtures', () => {
  const test = Object.assign((title: string, body: unknown) => registered.set(title, body), {
    describe: (_title: string, body: () => void) => body(),
    use: () => {},
  })
  return {
    processTest: test,
    expect: () => ({ toBeVisible: async () => {}, toHaveText: async () => {} }),
    ensureWorkerOnline: vi.fn(),
    restartWorker: vi.fn(),
  }
})
vi.mock('./helpers/ui', async original => ({
  ...await original<typeof import('./helpers/ui')>(),
  sendMessage: vi.fn(async () => {}),
  waitForAgentIdle: vi.fn(async () => {}),
  loginViaToken: vi.fn(async () => {}),
  openWorkspace: vi.fn(async () => {}),
}))

describe('agent input queue context cleanup', () => {
  it('closes the second context when page setup fails in the actual registered callback', async () => {
    await import('./108-agent-input-queue.spec')
    const title = 'persists paused input across clients, a reload, and a Worker restart, then supports queue changes'
    const run = registered.get(title)
    if (typeof run !== 'function')
      throw new Error('The actual retained queue test callback is absent.')
    const failed = new Error('The controlled second page setup failed.')
    const secondContext = {
      newPage: vi.fn(async () => {
        throw failed
      }),
      close: vi.fn(async () => {}),
    }
    const page = {
      locator: vi.fn(() => ({})),
      getByTestId: vi.fn(() => ({ click: vi.fn(async () => {}) })),
    }
    const browser = { newContext: vi.fn(async () => secondContext) }
    const modelScript = {
      queue: vi.fn(async () => {}),
      prompt: (text: string) => `controlled native prompt: ${text}`,
      waitForSteps: vi.fn(async () => {}),
    }
    await expect(run({
      page,
      browser,
      modelScript,
      authenticatedWorkspace: { workspaceId: 'retained-workspace' },
      separateHubWorker: { hubUrl: 'http://127.0.0.1:1', adminToken: 'private-test-token' },
    })).rejects.toBe(failed)
    expect(browser.newContext).toHaveBeenCalledTimes(1)
    expect(secondContext.newPage).toHaveBeenCalledTimes(1)
    expect(secondContext.close).toHaveBeenCalledTimes(1)
  })
})
