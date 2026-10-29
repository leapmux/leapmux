import { describe, expect, it } from 'bun:test'
import { parseMeminfo, parseProcNetTcp, parseProcStat, parseProcStatCpu } from './linux.mjs'

describe('parseProcStat', () => {
  it('reads the fields after a command name that holds spaces and parentheses', () => {
    const fields = [
      'S',
      '41',
      ...Array.from({ length: 19 }, (_, index) => String(index)),
      '345',
      '1',
    ]
    expect(parseProcStat(`1234 (a (weird) name) ${fields.join(' ')}`)).toEqual({
      comm: 'a (weird) name',
      state: 'S',
      parent: 41,
      rssPages: 345,
    })
  })

  it('rejects text without a command name', () => {
    expect(parseProcStat('1234 S 41')).toBeNull()
  })
})

describe('parseMeminfo', () => {
  it('converts kilobyte values to bytes and keeps plain counts', () => {
    const values = parseMeminfo('MemTotal:       16384 kB\nMemAvailable:    8192 kB\nHugePages_Total:       0\n')
    expect(values.get('MemTotal')).toBe(16384 * 1024)
    expect(values.get('MemAvailable')).toBe(8192 * 1024)
    expect(values.get('HugePages_Total')).toBe(0)
  })
})

describe('parseProcNetTcp', () => {
  it('counts IPv4 and IPv6 connections by state', () => {
    const header = '  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n'
    const counts = parseProcNetTcp(`${header}   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000 0 1\n   1: 0100007F:A1B2 0100007F:1F90 06 00000000:00000000 00:00000000 00000000  1000 0 2\n`)
    parseProcNetTcp(`${header}   0: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 01 0 0 0 0 0 3\n`, counts)
    expect([...counts]).toEqual([
      ['Listen', 1],
      ['TimeWait', 1],
      ['Established', 1],
    ])
  })
})

describe('parseProcStat process state', () => {
  it.each(['S', 'Z', 'X'])('reads state %s after a parenthesized command', (state) => {
    expect(parseProcStat(`1 (a (nested) name) ${state}   42\t0 0`).state).toBe(state)
    expect(parseProcStat(`1 (a (nested) name) ${state}   42\t0 0`).parent).toBe(42)
  })

  it('rejects missing command delimiters', () => {
    expect(parseProcStat('1 missing) S 42')).toBeNull()
    expect(parseProcStat('1 (missing S 42')).toBeNull()
  })
})

describe('parseProcStatCpu', () => {
  it('reads idle and total CPU counters', () => {
    expect(parseProcStatCpu('cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3\n')).toEqual({ idle: 800, total: 1000 })
    expect(parseProcStatCpu('cpu  200 0 200 1300 300 0 0 0 0 0\n')).toEqual({ idle: 1600, total: 2000 })
  })
})
