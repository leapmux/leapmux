import type { Page } from '@playwright/test'
import type { SoloServerHandle } from './devServer'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { startSoloServer, stopSoloServer } from './devServer'
import { completeSoloPasswordSetup, signInToSoloViaUI, SOLO_PASSWORD, soloServerFixtures } from './solo'

vi.mock('./devServer', () => ({ startSoloServer: vi.fn(), stopSoloServer: vi.fn(async () => {}) }))

/**
 * A fake page whose locators record each action in `log`. Each assertion records its expression and the locator path,
 * and `answer` decides it from the path.
 */
function fakePage(log: string[], answer: (expression: string, path: string) => boolean = () => true): Page {
  class FakeLocator {
    readonly _apiName = 'Locator'
    constructor(readonly path: string) {}
    getByTestId(id: string) {
      return new FakeLocator(`${this.path} >> testid=${id}`)
    }

    getByLabel(label: string) {
      return new FakeLocator(`${this.path} >> label=${label}`)
    }

    getByRole(role: string, options: { name: string }) {
      return new FakeLocator(`${this.path} >> role=${role}[${options.name}]`)
    }

    async fill(value: string) {
      log.push(`fill ${this.path} with ${value}`)
    }

    async click() {
      log.push(`click ${this.path}`)
    }

    async reload() {
      log.push('reload')
    }

    context() {
      return { clearCookies: async () => {
        log.push('clear cookies')
      } }
    }

    async _expect(expression: string, options: { isNot: boolean, expectedValue?: unknown, expressionArg?: unknown }) {
      log.push(`${options.isNot ? 'not ' : ''}${expression} ${this.path}`)
      const matches = answer(expression, this.path)
      return { matches, received: matches, log: [], timedOut: false }
    }
  }
  return new FakeLocator('page') as unknown as Page
}

beforeEach(() => {
  vi.mocked(startSoloServer).mockReset()
  vi.mocked(stopSoloServer).mockReset().mockResolvedValue(undefined)
})

describe('completeSoloPasswordSetup', () => {
  const gate = 'page >> testid=password-setup-gate'

  it('fills both fields of the gate, submits once, and requires the gate to go away', async () => {
    const log: string[] = []
    await completeSoloPasswordSetup(fakePage(log))
    expect(log).toEqual([
      `to.be.visible ${gate}`,
      `fill ${gate} >> label=New Password with ${SOLO_PASSWORD}`,
      `fill ${gate} >> label=Confirm Password with ${SOLO_PASSWORD}`,
      `click ${gate} >> role=button[Set Password]`,
      `to.be.hidden ${gate}`,
    ])
  })

  it('types nothing when no gate shows', async () => {
    const log: string[] = []
    await expect(completeSoloPasswordSetup(fakePage(log, () => false))).rejects.toThrow('a TCP caller meets the password-setup gate')
    expect(log).toEqual([`to.be.visible ${gate}`])
  })
})

describe('signInToSoloViaUI', () => {
  const signIn = 'page >> role=button[Sign in]'

  it('drops the session, reloads, checks the fixed username, and signs in with the password', async () => {
    const log: string[] = []
    await signInToSoloViaUI(fakePage(log))
    expect(log).toEqual([
      'clear cookies',
      'reload',
      `to.be.visible ${signIn}`,
      'to.have.value page >> label=Username',
      'to.have.attribute.value page >> label=Username',
      `fill page >> label=Password with ${SOLO_PASSWORD}`,
      `click ${signIn}`,
      `to.be.hidden ${signIn}`,
    ])
  })

  it('fails before it types when the reload shows no sign-in form', async () => {
    const log: string[] = []
    await expect(signInToSoloViaUI(fakePage(log, (expression, path) => !(expression === 'to.be.visible' && path === signIn))))
      .rejects
      .toThrow('a browser with no session meets the sign-in form')
    expect(log.some(entry => entry.startsWith('fill'))).toBe(false)
  })
})

describe('soloServerFixtures', () => {
  type SoloFixture = (args: { soloListenHost: string }, use: (solo: SoloServerHandle) => Promise<void>) => Promise<void>
  const soloServer = soloServerFixtures.soloServer as unknown as SoloFixture
  const handle = { hubUrl: 'http://127.0.0.1:1', listen: '127.0.0.1:1' } as SoloServerHandle

  it('listens on loopback unless the spec asks for another host', () => {
    expect(soloServerFixtures.soloListenHost).toEqual(['127.0.0.1', { option: true }])
  })

  it('starts the hub on the host of the spec, and stops it after the test', async () => {
    vi.mocked(startSoloServer).mockResolvedValue(handle)
    const use = vi.fn(async () => {
      expect(stopSoloServer).not.toHaveBeenCalled()
    })
    await soloServer({ soloListenHost: '0.0.0.0' }, use)
    expect(startSoloServer).toHaveBeenCalledWith({ listenHost: '0.0.0.0' })
    expect(use).toHaveBeenCalledWith(handle)
    expect(stopSoloServer).toHaveBeenCalledWith(handle)
  })

  it('stops the hub after a failed test, and reports the failure of the test', async () => {
    vi.mocked(startSoloServer).mockResolvedValue(handle)
    const failure = new Error('The test failed.')
    await expect(soloServer({ soloListenHost: '127.0.0.1' }, async () => {
      throw failure
    })).rejects.toBe(failure)
    expect(stopSoloServer).toHaveBeenCalledWith(handle)
  })
})
