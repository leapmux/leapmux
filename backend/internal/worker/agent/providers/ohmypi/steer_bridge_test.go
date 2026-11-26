package ohmypi

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserveOMPSteerCapability(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "steer-ready", Prefix: "steer-prefix:"}

	// Non-"extension" sources must be ignored even when the command name matches.
	r.emit(`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"builtin"},{"name":"steer-ready","source":"user"}]}`)
	r.agent.Mu.Lock()
	assert.False(t, r.agent.steerBridgeReady, "non-extension command source must not enable steer bridge")
	r.agent.Mu.Unlock()

	// Malformed JSON preserves the current false state.
	r.agent.observeOMPSteerCapability([]byte(`{not-json`))
	r.agent.Mu.Lock()
	assert.False(t, r.agent.steerBridgeReady)
	r.agent.Mu.Unlock()

	// Matching "extension" source enables steerBridgeReady.
	r.emit(`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"extension"}]}`)
	r.agent.Mu.Lock()
	assert.True(t, r.agent.steerBridgeReady)
	r.agent.Mu.Unlock()

	// Malformed JSON preserves the current true state.
	r.agent.observeOMPSteerCapability([]byte(`{"commands": "invalid"}`))
	r.agent.Mu.Lock()
	assert.True(t, r.agent.steerBridgeReady, "malformed payload must preserve steerBridgeReady")
	r.agent.Mu.Unlock()

	// Subsequent update without the capability flips steerBridgeReady back to false.
	r.emit(`{"type":"available_commands_update","commands":[{"name":"other-cmd","source":"extension"}]}`)
	r.agent.Mu.Lock()
	assert.False(t, r.agent.steerBridgeReady, "update without capability must disable steer bridge")
	r.agent.Mu.Unlock()
}

func setupSteerIdentityNodeEnv(t *testing.T, coreIndex, codingIndex string) (string, string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	dir := t.TempDir()
	codingPkg := filepath.Join(dir, "node_modules", "@oh-my-pi", "pi-coding-agent")
	corePkg := filepath.Join(dir, "node_modules", "@oh-my-pi", "pi-agent-core")
	require.NoError(t, os.MkdirAll(codingPkg, 0o755))
	require.NoError(t, os.MkdirAll(corePkg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(codingPkg, "package.json"), []byte(`{"name":"@oh-my-pi/pi-coding-agent","type":"module","main":"index.mjs"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(corePkg, "package.json"), []byte(`{"name":"@oh-my-pi/pi-agent-core","type":"module","main":"index.mjs"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(corePkg, "index.mjs"), []byte(coreIndex), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(codingPkg, "index.mjs"), []byte(codingIndex), 0o600))
	return nodePath, dir
}

func TestOMPSteerIdentityExtensionBehavior(t *testing.T) {
	t.Parallel()
	nodePath, dir := setupSteerIdentityNodeEnv(t, `
export class Agent {
  constructor() { this.promptCalls = [] }
  prompt(messages, ...rest) { this.promptCalls.push({ messages, rest }); return 'agent-ok' }
}
`, `
export class ExtensionRunner {
  constructor() { this.calls = [] }
  async emitInput(text, images, source, ...rest) {
    this.calls.push({ text, images, source, rest })
    if (text === 'return-null') return null
    return { text, images, handled: false }
  }
}
export class AgentSession {
  constructor(agent) { this.agent = agent; this.calls = [] }
  async prompt(text, options, ...rest) {
    this.calls.push({ text, options, rest })
    if (options?.triggerQueued !== undefined) {
      return await options.onPromptQueued.call({ ctx: 'queued-this' }, options.triggerQueued, 'q-extra')
    }
    if (options?.callAgentPrompt) {
      return this.agent.prompt(options.callAgentPrompt)
    }
    return { text, images: options?.images, rest }
  }
}
`)

	b1Dir := filepath.Join(dir, "b1")
	b2Dir := filepath.Join(dir, "b2")
	require.NoError(t, os.MkdirAll(b1Dir, 0o755))
	require.NoError(t, os.MkdirAll(b2Dir, 0o755))
	path1, cfg1, err := writeOMPSteerBridge(b1Dir)
	require.NoError(t, err)
	path2, cfg2, err := writeOMPSteerBridge(b2Dir)
	require.NoError(t, err)

	runner := filepath.Join(dir, "verify.mjs")
	script := `
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'
import { AgentSession, ExtensionRunner } from '@oh-my-pi/pi-coding-agent'
import { Agent } from '@oh-my-pi/pi-agent-core'

const ext1 = (await import(pathToFileURL(process.argv[2]).href)).default
const ext2 = (await import(pathToFileURL(process.argv[3]).href)).default
const prefix1 = process.argv[4]
const prefix2 = process.argv[5]
const field = process.argv[6]
const id = 'b'.repeat(48)

const cmds = []
ext1({ on: () => {}, registerCommand: (n) => cmds.push(n) })
// Reloading with a new nonce must not throw or double-wrap.
ext2({ on: () => {}, registerCommand: (n) => cmds.push(n) })
assert.equal(cmds.length, 2)

const er = new ExtensionRunner()
// Null result from emitInput must not throw and must invoke underlying emitInput once.
assert.equal(await er.emitInput(prefix2 + id + '\nreturn-null', undefined, 'rpc', 'extra-emit'), null)
assert.equal(er.calls.length, 1, 'reloaded extension must not double-wrap emitInput')
assert.deepEqual(er.calls.at(-1).rest, ['extra-emit'])

// Old prefix from ext1 must be rejected after ext2 reloads.
const oldInput = prefix1 + id + '\nold-prefix-msg'
const oldEmitted = await er.emitInput(oldInput, undefined, 'rpc')
assert.equal(oldEmitted.text, oldInput, 'old prefix must pass through unstripped')
assert.equal(oldEmitted.images, undefined, 'old prefix must not carry transport symbol')

// Undefined images must be restored to undefined inside AgentSession.prototype.prompt.
const emitted = await er.emitInput(prefix2 + id + '\nhello', undefined, 'rpc', 'extra-2')
assert.equal(emitted.text, 'hello')
assert.deepEqual(er.calls.at(-1).rest, ['extra-2'])

const agent = new Agent()
const session = new AgentSession(agent)
const res = await session.prompt(emitted.text, { streamingBehavior: 'steer', images: emitted.images }, 'p-extra')
assert.equal(session.calls.length, 1, 'reloaded extension must not double-wrap AgentSession.prototype.prompt')
assert.equal(res.images, undefined, 'absent images must not stay coerced to []')
assert.deepEqual(res.rest, ['p-extra'])

// onPromptQueued must preserve this, trailing args, return value/Promise, and handle non-array messages.
const singleMsg = { role: 'user', attribution: 'user', content: 'hi' }
const queuedRes = await session.prompt(emitted.text, {
  streamingBehavior: 'steer',
  images: emitted.images,
  triggerQueued: singleMsg,
  async onPromptQueued(messages, extra) {
    return { self: this?.ctx, messages, extra }
  },
})
assert.equal(singleMsg[field], id)
assert.equal(queuedRes.self, 'queued-this')
assert.equal(queuedRes.extra, 'q-extra')
`
	require.NoError(t, os.WriteFile(runner, []byte(script), 0o600))
	cmd := exec.Command(nodePath, runner, path1, path2, cfg1.Prefix, cfg2.Prefix, cfg1.MessageField)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "node verification failed:\n%s", string(out))
}

func TestOMPSteerIdentityExtensionEdgeCases(t *testing.T) {
	t.Parallel()
	nodePath, dir := setupSteerIdentityNodeEnv(t, `
export class Agent {
  constructor(name) { this.name = name; this.promptCalls = [] }
  prompt(messages, ...rest) { this.promptCalls.push({ messages, rest }); return 'agent-ok' }
}
`, `
export class ExtensionRunner {
  constructor() { this.calls = []; this.nextResult = null }
  async emitInput(text, images, source, ...rest) {
    this.calls.push({ text, images, source, rest })
    if (this.nextResult !== null) {
      const res = this.nextResult
      this.nextResult = null
      return res
    }
    return { text, images, handled: false }
  }
}
export class AgentSession {
  constructor(agent) { this.agent = agent; this.calls = [] }
  async prompt(text, options, ...rest) {
    this.calls.push({ text, options, rest })
    if (options?.run) {
      return await options.run(this, text, options)
    }
    return { text, images: options?.images, rest }
  }
}
`)

	bridgeDir := filepath.Join(dir, "bridge")
	require.NoError(t, os.MkdirAll(bridgeDir, 0o755))
	bridgePath, cfg, err := writeOMPSteerBridge(bridgeDir)
	require.NoError(t, err)

	runner := filepath.Join(dir, "verify_edges.mjs")
	script := `
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'
import { AgentSession, ExtensionRunner } from '@oh-my-pi/pi-coding-agent'
import { Agent } from '@oh-my-pi/pi-agent-core'

const ext = (await import(pathToFileURL(process.argv[2]).href)).default
const prefix = process.argv[3]
const childPrefix = process.argv[4]
const field = process.argv[5]
const id = 'a'.repeat(48)
const id2 = 'c'.repeat(48)

ext({ on: () => {}, registerCommand: () => {} })

const er = new ExtensionRunner()
const agent = new Agent('main')
const otherAgent = new Agent('other')
const session = new AgentSession(agent)

// 1. emitInput: source !== 'rpc' passes through untouched.
const nonRpcText = prefix + id + '\nhello'
const nonRpc = await er.emitInput(nonRpcText, undefined, 'interactive')
assert.equal(nonRpc.text, nonRpcText)
assert.equal(nonRpc.images, undefined)

// 2. emitInput: { handled: true } returns as-is without adding transport symbol.
er.nextResult = { handled: true, text: 'hook-handled' }
const handledRes = await er.emitInput(prefix + id + '\nhello', undefined, 'rpc')
assert.deepEqual(handledRes, { handled: true, text: 'hook-handled' })
assert.equal(handledRes.images, undefined)

// 3. emitInput: explicit images preserved and NOT restored to undefined in session.prompt.
const callerImgs = [{ type: 'image', data: 'abc' }]
const withCallerImgs = await er.emitInput(prefix + id + '\nwith-img', callerImgs, 'rpc')
assert.deepEqual(withCallerImgs.images, callerImgs)
assert.notEqual(withCallerImgs.images, callerImgs, 'forwarded images must be a fresh copy')
const imgPromptRes = await session.prompt(withCallerImgs.text, {
  streamingBehavior: 'steer',
  images: withCallerImgs.images,
  run: (_s, text, opts) => ({ text, images: opts.images }),
})
assert.deepEqual(imgPromptRes.images, callerImgs)

// 4. emitInput: hook modifying text keeps modified text; hook omitting text falls back to identity.text.
er.nextResult = { text: 'hook-modified', handled: false }
const modRes = await er.emitInput(prefix + id + '\noriginal', undefined, 'rpc')
assert.equal(modRes.text, 'hook-modified')

er.nextResult = { handled: false }
const omitRes = await er.emitInput(prefix + id + '\nkept-body', undefined, 'rpc')
assert.equal(omitRes.text, 'kept-body')

// 5. readIdentity invalid shapes via emitInput('rpc').
for (const bad of [
  null,
  123,
  prefix + id,                        // missing newline
  prefix + 'a'.repeat(47) + '\nbody', // too short
  prefix + 'a'.repeat(49) + '\nbody', // too long
  prefix + 'a'.repeat(47) + '-\nbody', // non-alphanumeric
]) {
  const out = await er.emitInput(bad, undefined, 'rpc')
  assert.equal(out.text, bad, 'invalid identity shape must pass through unchanged')
  assert.equal(out.images, undefined)
}

// 6. Agent.prototype.prompt: single message & array tagged when owner.agent === this,
//    different Agent not tagged, already-tagged owner not re-tagged.
{
  const e = await er.emitInput(prefix + id + '\nsingle-agent', undefined, 'rpc')
  const msgSingle = { role: 'user', attribution: 'user', content: 'one' }
  const msgSecond = { role: 'user', attribution: 'user', content: 'two' }
  const msgOther = { role: 'user', attribution: 'user', content: 'other' }
  await session.prompt(e.text, {
    streamingBehavior: 'steer',
    images: e.images,
    run: (s) => {
      otherAgent.prompt(msgOther)
      assert.equal(msgOther[field], undefined, 'different Agent instance must not be tagged')
      s.agent.prompt(msgSingle)
      assert.equal(msgSingle[field], id, 'single message on owner.agent must be tagged')
      s.agent.prompt(msgSecond)
      assert.equal(msgSecond[field], undefined, 'already-tagged owner must not re-tag')
    },
  })
}

{
  // Array tagging + tagUserMessage skipping invalid entries and tagging only first valid user message.
  const e = await er.emitInput(prefix + id2 + '\narray-agent', undefined, 'rpc')
  const nonUser = { role: 'assistant', attribution: 'user', content: 'a' }
  const sysAttr = { role: 'user', attribution: 'system', content: 'b' }
  const firstUser = { role: 'user', attribution: 'user', content: 'c' }
  const secondUser = { role: 'user', attribution: 'user', content: 'd' }
  await session.prompt(e.text, {
    streamingBehavior: 'steer',
    images: e.images,
    run: (s) => {
      s.agent.prompt([null, 'primitive', 42, nonUser, sysAttr, firstUser, secondUser])
    },
  })
  assert.equal(nonUser[field], undefined)
  assert.equal(sysAttr[field], undefined)
  assert.equal(firstUser[field], id2, 'first valid user message in array must be tagged')
  assert.equal(secondUser[field], undefined, 'second valid user message in array must not be tagged')
}

// 7. Child steer: throwOnDrop: true + ChildPrefix strips prefix & tags; throwOnDrop: false does neither.
{
  const childMsg = { role: 'user', attribution: 'user', content: 'child-body' }
  const childRes = await session.prompt(childPrefix + id + '\nchild-body', {
    streamingBehavior: 'steer',
    throwOnDrop: true,
    run: (s, text, opts) => {
      opts.onPromptQueued([null, childMsg])
      return { text }
    },
  })
  assert.equal(childRes.text, 'child-body')
  assert.equal(childMsg[field], id)

  const noDropMsg = { role: 'user', attribution: 'user', content: 'no-drop' }
  const rawChild = childPrefix + id + '\nno-drop'
  const noDropRes = await session.prompt(rawChild, {
    streamingBehavior: 'steer',
    throwOnDrop: false,
    run: (s, text) => {
      s.agent.prompt(noDropMsg)
      return { text }
    },
  })
  assert.equal(noDropRes.text, rawChild, 'throwOnDrop:false must not strip childPrefix')
  assert.equal(noDropMsg[field], undefined, 'throwOnDrop:false must not tag message')
}

// 8. Missing onPromptQueued: wrapped onPromptQueued still tags and returns undefined without throwing.
{
  const e = await er.emitInput(prefix + id + '\nno-cb', undefined, 'rpc')
  const queuedMsg = { role: 'user', attribution: 'user', content: 'q' }
  const ret = await session.prompt(e.text, {
    streamingBehavior: 'steer',
    images: e.images,
    run: (_s, _text, opts) => opts.onPromptQueued(queuedMsg),
  })
  assert.equal(ret, undefined)
  assert.equal(queuedMsg[field], id)
}

// 9. Non-steer AgentSession.prototype.prompt resets ALS scope so nested calls do not inherit outer steer id.
{
  const e = await er.emitInput(prefix + id + '\nouter', undefined, 'rpc')
  const nestedMsg = { role: 'user', attribution: 'user', content: 'nested' }
  const outerMsg = { role: 'user', attribution: 'user', content: 'outer' }
  await session.prompt(e.text, {
    streamingBehavior: 'steer',
    images: e.images,
    run: async (s) => {
      await s.prompt('nested-follow-up', {
        streamingBehavior: 'followUp',
        run: (inner) => {
          inner.agent.prompt(nestedMsg)
        },
      })
      // After nested non-steer completes, outer ALS scope still tags outerMsg.
      s.agent.prompt(outerMsg)
    },
  })
  assert.equal(nestedMsg[field], undefined, 'non-steer prompt must reset ALS scope')
  assert.equal(outerMsg[field], id, 'outer steer scope remains active after nested non-steer returns')
}
`
	require.NoError(t, os.WriteFile(runner, []byte(script), 0o600))
	cmd := exec.Command(nodePath, runner, bridgePath, cfg.Prefix, cfg.ChildPrefix, cfg.MessageField)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "node edge-case verification failed:\n%s", string(out))
}
