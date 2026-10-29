import { Buffer } from 'node:buffer'
import { spawn, spawnSync } from 'node:child_process'
import { EventEmitter } from 'node:events'
import process from 'node:process'
import { Writable } from 'node:stream'
import { describe, expect, it } from 'bun:test'
import { runWithTimeout } from './run-renovate.mjs'

async function exitedProcessTree(detached) {
  let leader
  let leaderPid
  let descendant
  let expire
  let stoppedGroup
  let reportExit
  let output = ''
  const exited = new Promise((resolve) => {
    reportExit = resolve
  })
  const source = `const {spawn}=require('node:child_process'); const child=spawn(process.execPath,['-e','process.send("ready"); setInterval(()=>{},1000)'],{detached:${detached},stdio:['ignore','inherit','inherit','ipc']}); child.once('message',()=>process.stdout.write(String(child.pid)+'\\n',()=>process.exit(0)));`
  const running = runWithTimeout(process.execPath, ['-e', source], {
    stdio: ['ignore', 'pipe', 'pipe'],
    output: {
      write: (data) => {
        output += data
        if (output.includes('\n'))
          descendant = Number(output.split('\n')[0])
      },
    },
    runtime: {
      spawn: (...args) => {
        leader = spawn(...args)
        leaderPid = leader.pid
        leader.once('exit', reportExit)
        return leader
      },
      kill: (pid, signal) => {
        stoppedGroup = pid
        return process.kill(pid, signal)
      },
      setTimeout: (callback) => {
        expire = callback
        return 0
      },
      clearTimeout: () => {},
    },
  })
  await exited
  return {
    leader,
    leaderPid,
    running,
    expire,
    stoppedGroup: () => stoppedGroup,
    cleanup: async () => {
      for (const pid of [
        ...(stoppedGroup
          ? []
          : [-leaderPid]),
        ...(detached
          ? [descendant]
          : []),
      ]) {
        if (!Number.isInteger(pid) || pid === 0)
          continue
        try {
          process.kill(pid, 'SIGKILL')
        }
        catch (error) {
          if (error.code !== 'ESRCH')
            throw error
        }
      }
      await running
    },
  }
}

describe('runWithTimeout', () => {
  it.skipIf(process.platform === 'win32')('stops a real Node child after an asynchronous output failure', () => {
    const source = `import {spawn} from 'node:child_process'; import {Writable} from 'node:stream'; import {runWithTimeout} from ${JSON.stringify(new URL('./run-renovate.mjs', import.meta.url).href)};
      let child; let exited; const sink=new Writable({write(_data,_encoding,callback){process.nextTick(()=>callback(Object.assign(new Error('closed consumer'),{code:'EPIPE'})))}});
      try {await runWithTimeout(process.execPath,['-e','console.log("ready"); setInterval(()=>{},1000)'],{stdio:['ignore','pipe','pipe'],output:{write(){}},streams:{stdout:sink,stderr:sink},runtime:{spawn(...args){child=spawn(...args);exited=new Promise(resolve=>child.once('exit',resolve));return child},kill:process.kill,setTimeout,clearTimeout}});process.exitCode=2}
      catch(error){child.ref(); await exited; console.log(JSON.stringify({message:error.message,signal:child.signalCode,listeners:sink.listenerCount('error')}))}`
    const result = spawnSync('node', ['--input-type=module', '-e', source], { encoding: 'utf8', timeout: 10000 })
    expect(result.status, result.stderr).toBe(0)
    expect(JSON.parse(result.stdout)).toMatchObject({ message: 'closed consumer', signal: 'SIGKILL', listeners: 0 })
  })
  it('handles child output errors until the source closes', async () => {
    const child = Object.assign(new EventEmitter(), {
      pid: 42,
      exitCode: null,
      signalCode: null,
      stdout: new EventEmitter(),
      stderr: new EventEmitter(),
      unref() {},
    })
    child.stdout.destroy = child.stderr.destroy = () => {}
    const sink = new Writable({
      write(_data, _encoding, callback) {
        callback()
      },
    })
    const result = runWithTimeout('fixture', [], {
      output: {
        write() {},
      },
      streams: { stdout: sink, stderr: sink },
      runtime: {
        spawn: () => child,
        kill() {},
        setTimeout: () => 0,
        clearTimeout() {},
      },
    })
    child.stderr.emit('error', new Error('child stream failed'))
    await expect(result).rejects.toThrow('child stream failed')
    child.stderr.emit('error', new Error('later source error'))
    child.stderr.emit('close')
    expect(child.stderr.listenerCount('error')).toBe(0)
  })
  it('rejects asynchronous forwarding failures even after the child closes and releases stream listeners', async () => {
    let finishWrite
    let stopped = 0
    const destination = new Writable({
      write(_data, _encoding, callback) {
        finishWrite = callback
      },
    })
    const child = Object.assign(new EventEmitter(), {
      pid: 42,
      exitCode: null,
      signalCode: null,
      stdout: new EventEmitter(),
      stderr: new EventEmitter(),
      unref() {},
    })
    child.stdout.destroy = child.stderr.destroy = () => {}
    const result = runWithTimeout('fixture', [], {
      output: {
        write() {},
      },
      streams: { stdout: destination, stderr: destination },
      runtime: {
        spawn: () => child,
        kill() { stopped++ },
        setTimeout: () => 0,
        clearTimeout() {},
      },
    })
    child.stdout.emit('data', Buffer.from('fixture'))
    child.emit('close', 0, null)
    expect(finishWrite).toBeFunction()
    finishWrite(Object.assign(new Error('asynchronous pipe failure'), { code: 'EPIPE' }))
    await expect(result).rejects.toThrow('asynchronous pipe failure')
    await new Promise(resolve => setImmediate(resolve))
    expect(destination.listenerCount('error')).toBe(0)
    if (process.platform !== 'win32')
      expect(stopped).toBe(1)
  })
  it('preserves the original diagnostic failure when termination emits another error', async () => {
    const child = new EventEmitter()
    Object.assign(child, {
      pid: 42,
      stdout: new EventEmitter(),
      stderr: new EventEmitter(),
      unref() {},
    })
    child.stdout.destroy = child.stderr.destroy = () => {}
    const result = runWithTimeout('fixture', [], {
      output: {
        write() { throw new Error('original diagnostic failure') },
      },
      runtime: {
        spawn: () => child,
        kill() { child.emit('error', new Error('termination failure')) },
        setTimeout: () => 0,
        clearTimeout() {},
      },
    })
    child.stdout.emit('data', Buffer.from('diagnostic'))
    await expect(result).rejects.toThrow('original diagnostic failure')
  })
  it('reports cancellation signals separately from the execution deadline', () => {
    for (const [signal, code] of [
      ['SIGINT', 130],
      ['SIGTERM', 143],
    ]) {
      const source = `import {EventEmitter} from 'node:events'; import {runWithTimeout} from ${JSON.stringify(new URL('./run-renovate.mjs', import.meta.url).href)}; const child=Object.assign(new EventEmitter(),{pid:42,unref(){}}); const result=runWithTimeout('fixture',[],{runtime:{spawn:()=>child,kill(){},setTimeout:()=>0,clearTimeout(){}}}); process.emit(${JSON.stringify(signal)}); console.log(JSON.stringify(await result));`
      const result = spawnSync('node', ['--input-type=module', '-e', source], { encoding: 'utf8' })
      expect(result.status, result.stderr).toBe(0)
      expect(JSON.parse(result.stdout)).toMatchObject({ code, signal, timedOut: false })
    }
  })
  it.skipIf(process.platform === 'win32')('terminates a POSIX process group after its leader exits with inherited pipes open', async () => {
    const fixture = await exitedProcessTree(false)
    try {
      expect(fixture.leader.exitCode).toBe(0)
      fixture.expire()
      expect(fixture.stoppedGroup()).toBe(-fixture.leaderPid)
      expect(await fixture.running).toMatchObject({ code: 124, timedOut: true })
    }
    finally { await fixture.cleanup() }
  })
  it.skipIf(process.platform === 'win32')('completes the deadline even when a detached descendant retains an output pipe', async () => {
    const fixture = await exitedProcessTree(true)
    let completed = false
    fixture.running.then(() => {
      completed = true
    })
    try {
      fixture.expire()
      await Promise.resolve()
      expect(completed).toBe(true)
      expect(await fixture.running).toMatchObject({ code: 124, timedOut: true })
    }
    finally { await fixture.cleanup() }
  })
  it('preserves successful and failed CLI exit codes', async () => {
    for (const code of [0, 17]) {
      const result = await runWithTimeout(process.execPath, ['-e', `process.exit(${code})`], { stdio: 'ignore' })
      expect(result.code).toBe(code)
      expect(result.timedOut).toBe(false)
    }
  })
  it('kills an execution that exceeds its deadline and returns a failure', async () => {
    const result = await runWithTimeout(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { timeoutMs: 100, stdio: 'ignore' })
    expect(result.code).toBe(124)
    expect(result.timedOut).toBe(true)
  })
  it('rejects invalid deadlines and unavailable executables', async () => {
    for (const timeoutMs of [0, -1, NaN, Infinity, 1.5])
      await expect(runWithTimeout('unused', [], { timeoutMs })).rejects.toThrow()
    await expect(runWithTimeout('leapmux-no-such-command', [], { stdio: 'ignore' })).rejects.toThrow()
  })
  it('rejects a timeout that exceeds the timer maximum', async () => {
    await expect(runWithTimeout('unused', [], { timeoutMs: 2147483648, stdio: 'ignore' })).rejects.toThrow('timeout must')
  })
  it('reports a failed diagnostic write and stops the execution', async () => {
    await expect(runWithTimeout(process.execPath, ['-e', 'console.log("fixture output"); setInterval(() => {}, 1000)'], {
      stdio: ['ignore', 'pipe', 'pipe'],
      output: {
        write: () => { throw new Error('diagnostic write failed') },
      },
    })).rejects.toThrow('diagnostic write failed')
  })
})
