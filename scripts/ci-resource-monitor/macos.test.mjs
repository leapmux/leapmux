import process from 'node:process'
import { describe, expect, it } from 'bun:test'
import { parseNetstat, parsePs, parseSwapSize, parseSysctl, parseVmStat, runTool } from './macos.mjs'

describe('parsePs', () => {
  it('reads each row and keeps the base name of a path that holds spaces', () => {
    const rows = parsePs('    1     0  12000   0.5 /sbin/launchd\n  321     1   2048  12.0 /Applications/Some App.app/Contents/MacOS/Some App\n')
    expect(rows).toEqual([
      {
        name: 'launchd',
        pid: 1,
        parent: 0,
        memory: 12000 * 1024,
        cpu: 0.5,
        start: 0,
        extra: [],
      },
      {
        name: 'Some App',
        pid: 321,
        parent: 1,
        memory: 2048 * 1024,
        cpu: 12,
        start: 0,
        extra: [],
      },
    ])
  })
})

describe('parseVmStat', () => {
  it('reads the page size and the page counts', () => {
    const { pageSize, pages } = parseVmStat('Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                               12345.\nPages occupied by compressor:              678.\n')
    expect(pageSize).toBe(16384)
    expect(pages.get('Pages free')).toBe(12345)
    expect(pages.get('Pages occupied by compressor')).toBe(678)
  })
})

describe('parseSysctl', () => {
  it('reads named values, and parseSwapSize reads the swap sizes', () => {
    const values = parseSysctl('hw.memsize: 17179869184\nvm.swapusage: total = 2048.00M  used = 1536.50M  free = 511.50M  (encrypted)\n')
    expect(values.get('hw.memsize')).toBe('17179869184')
    expect(parseSwapSize(values.get('vm.swapusage'), 'total')).toBe(2048 * 2 ** 20)
    expect(parseSwapSize(values.get('vm.swapusage'), 'used')).toBe(1536.5 * 2 ** 20)
    expect(parseSwapSize(undefined, 'used')).toBe(0)
  })
})

describe('parseNetstat', () => {
  it('counts the TCP connections by state and skips other lines', () => {
    const counts = parseNetstat([
      'Active Internet connections (including servers)',
      'Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)',
      'tcp4       0      0  127.0.0.1.8080         127.0.0.1.50000        ESTABLISHED',
      'tcp6       0      0  *.22                   *.*                    LISTEN',
      'tcp4       0      0  127.0.0.1.50001        127.0.0.1.8080         TIME_WAIT',
      'udp4       0      0  *.5353                 *.*',
    ].join('\n'))
    expect([...counts]).toEqual([
      ['Established', 1],
      ['Listen', 1],
      ['TimeWait', 1],
    ])
  })
})

describe('runTool', () => {
  it('rejects nonzero exits even when stdout contains valid-looking readings', () => {
    expect(() => runTool(process.execPath, ['-e', 'console.log("hw.memsize: 123"); console.error("denied"); process.exit(7)'])).toThrow(/7.*denied/)
  })

  it('rejects a command that a signal ends on Unix', () => {
    if (process.platform === 'win32')
      return
    expect(() => runTool(process.execPath, ['-e', 'process.kill(process.pid, "SIGTERM")'])).toThrow('SIGTERM')
  })

  it('keeps successful output and propagates a missing command', () => {
    expect(runTool(process.execPath, ['-e', 'process.stdout.write("reading")'])).toBe('reading')
    expect(() => runTool('definitely-no-such-command-for-the-monitor', [])).toThrow()
  })

  it('ignores optional missing sysctl keys on macOS', () => {
    if (process.platform !== 'darwin')
      return
    expect(parseSysctl(runTool('sysctl', ['-i', 'hw.memsize', 'leapmux.nonexistent.test.key'])).has('hw.memsize')).toBe(true)
  })
})
