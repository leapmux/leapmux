import { describe, expect, it } from 'vitest'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TERMINAL_DISCONNECTED, terminalStatusClassList } from './terminalStatus'
import * as styles from './terminalStatus.css'

describe('TERMINAL_DISCONNECTED', () => {
  // The display status joins the worker's enum in one union. A value that a
  // TerminalStatus member could also hold would make the two states one.
  it('equals no TerminalStatus member, by name or by number', () => {
    expect(Object.values(TerminalStatus)).not.toContain(TERMINAL_DISCONNECTED)
  })
})

describe('terminalStatusClassList', () => {
  it('fades a terminal the client marked disconnected', () => {
    expect(terminalStatusClassList(TERMINAL_DISCONNECTED)).toEqual({ [styles.disconnected]: true })
  })

  it('strikes through a terminal whose shell exited', () => {
    expect(terminalStatusClassList(TerminalStatus.EXITED)).toEqual({ [styles.exited]: true })
  })

  it.each([
    ['UNSPECIFIED', TerminalStatus.UNSPECIFIED],
    ['STARTING', TerminalStatus.STARTING],
    ['READY', TerminalStatus.READY],
    ['STARTUP_FAILED', TerminalStatus.STARTUP_FAILED],
    ['an absent status', undefined],
  ])('applies no class for %s', (_label, status) => {
    expect(terminalStatusClassList(status)).toEqual({})
  })

  // Every tab render reads this, so each status answers with one shared
  // object instead of a new one per call.
  it('returns the same object on every call for one status', () => {
    expect(terminalStatusClassList(TERMINAL_DISCONNECTED)).toBe(terminalStatusClassList(TERMINAL_DISCONNECTED))
    expect(terminalStatusClassList(TerminalStatus.EXITED)).toBe(terminalStatusClassList(TerminalStatus.EXITED))
    expect(terminalStatusClassList(TerminalStatus.READY)).toBe(terminalStatusClassList(undefined))
  })
})
