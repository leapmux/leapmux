package pi

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiSteerBridgeWritesSessionPrivateCapabilities(t *testing.T) {
	t.Parallel()
	firstDir, secondDir := t.TempDir(), t.TempDir()
	firstPath, first, err := writePiSteerBridge(firstDir)
	require.NoError(t, err)
	secondPath, second, err := writePiSteerBridge(secondDir)
	require.NoError(t, err)
	assert.NotEqual(t, first.Capability, second.Capability)
	assert.NotEqual(t, first.Prefix, second.Prefix)

	for _, session := range []struct {
		directory string
		path      string
		bridge    *piSteerBridge
	}{{firstDir, firstPath, first}, {secondDir, secondPath, second}} {
		assert.Equal(t, session.directory, filepath.Dir(session.path))
		raw, err := os.ReadFile(session.path)
		require.NoError(t, err)
		firstLine, program, found := strings.Cut(string(raw), "\n")
		require.True(t, found)
		const declaration = "const configuration = "
		require.True(t, strings.HasPrefix(firstLine, declaration))
		var config piSteerBridge
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(firstLine, declaration)), &config))
		assert.Equal(t, *session.bridge, config)
		assert.Equal(t, 48, config.IDLength)
		assert.Contains(t, program, "export default function")
	}
}

func TestPiSteerBridgeRefusesAnUnavailableDirectory(t *testing.T) {
	t.Parallel()
	path, bridge, err := writePiSteerBridge(filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "write the Pi steer identity extension")
	assert.Empty(t, path)
	assert.Nil(t, bridge)
}

func TestPiSteerIdentityExtensionBehavior(t *testing.T) {
	t.Parallel()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	dir := t.TempDir()
	// Provide a stub @earendil-works/pi-coding-agent package in node_modules.
	pkgDir := filepath.Join(dir, "node_modules", "@earendil-works", "pi-coding-agent")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","type":"module","main":"index.mjs"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "index.mjs"), []byte(`
export class AgentSession {
  constructor(agent) {
    this.agent = agent
    this.promptCalls = []
  }
  async prompt(text, options, ...rest) {
    this.promptCalls.push({ text, options, rest })
    if (options?.customRun) {
      return options.customRun(this, text, options, ...rest)
    }
    if (options?.callSteer) {
      return this.agent.steer(options.callSteer.message, ...(options.callSteer.extra ?? []))
    }
    if (options?.callPrompt) {
      return this.agent.prompt(options.callPrompt.messages, ...(options.callPrompt.extra ?? []))
    }
    return { text, rest }
  }
}
`), 0o600))

	bridgeDir1 := filepath.Join(dir, "bridge1")
	bridgeDir2 := filepath.Join(dir, "bridge2")
	require.NoError(t, os.MkdirAll(bridgeDir1, 0o755))
	require.NoError(t, os.MkdirAll(bridgeDir2, 0o755))
	path1, cfg1, err := writePiSteerBridge(bridgeDir1)
	require.NoError(t, err)
	path2, cfg2, err := writePiSteerBridge(bridgeDir2)
	require.NoError(t, err)

	runner := filepath.Join(dir, "verify.mjs")
	script := `
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'
import { AgentSession } from '@earendil-works/pi-coding-agent'

const ext1 = (await import(pathToFileURL(process.argv[2]).href)).default
const ext2 = (await import(pathToFileURL(process.argv[3]).href)).default
const prefix1 = process.argv[4]
const prefix2 = process.argv[5]
const field = process.argv[6]
const id = 'a'.repeat(48)

const originalPrompt = AgentSession.prototype.prompt
const cmds = []
ext1({ registerCommand: (name) => cmds.push(name) })
const wrappedAfterExt1 = AgentSession.prototype.prompt
assert.notEqual(wrappedAfterExt1, originalPrompt)
// Reloading with a second nonce must update config without double-wrapping.
ext2({ registerCommand: (name) => cmds.push(name) })
assert.equal(AgentSession.prototype.prompt, wrappedAfterExt1, 'reload must not double-wrap AgentSession.prototype.prompt')
assert.equal(cmds.length, 2)

function makeAgent() {
  const steered = []
  const prompted = []
  return {
    steered,
    prompted,
    steer(message, ...extra) { steered.push({ message, extra }); return 'ok-steer' },
    prompt(messages, ...extra) { prompted.push({ messages, extra }); return 'ok-prompt' },
  }
}

const agent1 = makeAgent()
const session = new AgentSession(agent1)

// 1. Non-string text must not throw.
const nonStr = await session.prompt(123, { source: 'rpc', streamingBehavior: 'steer' }, 'extra-1')
assert.deepEqual(nonStr.rest, ['extra-1'])

// 2. Identity preservation (WeakSet) + stripped prompt text + trailing args on agent.steer.
const trackedMsg = { role: 'user', content: 'hi' }
const seen = new WeakSet([trackedMsg])
await session.prompt(prefix2 + id + '\nhi', { source: 'rpc', streamingBehavior: 'steer', callSteer: { message: trackedMsg, extra: ['s-extra'] } }, 'p-extra')
assert.equal(agent1.steered.length, 1)
assert.equal(agent1.steered[0].message, trackedMsg, 'must preserve object identity for WeakSet')
assert.equal(seen.has(agent1.steered[0].message), true)
assert.equal(agent1.steered[0].message[field], id)
assert.deepEqual(agent1.steered[0].extra, ['s-extra'])
assert.equal(session.promptCalls.at(-1).text, 'hi')
assert.deepEqual(session.promptCalls.at(-1).rest, ['p-extra'])

// Nullish message must not throw and must pass null through unchanged.
await session.prompt(prefix2 + id + '\nhi', { source: 'rpc', streamingBehavior: 'steer', callSteer: { message: null } })
assert.equal(agent1.steered.at(-1).message, null)

// 3. Single non-array message passed to session.agent.prompt must still be tagged and preserve extra args.
const singleMsg = { role: 'user', content: 'single' }
await session.prompt(prefix2 + id + '\nsingle', { source: 'rpc', streamingBehavior: 'steer', callPrompt: { messages: singleMsg, extra: ['x'] } })
assert.equal(agent1.prompted[0].messages, singleMsg)
assert.equal(singleMsg[field], id)
assert.deepEqual(agent1.prompted[0].extra, ['x'])

// 4. Single-use tagging across an array [systemMsg, userMsg1, userMsg2] in currentAgent.prompt.
const systemMsg = { role: 'system', content: 'sys' }
const userMsg1 = { role: 'user', content: 'first' }
const userMsg2 = { role: 'user', content: 'second' }
await session.prompt(prefix2 + id + '\narray', {
  source: 'rpc',
  streamingBehavior: 'steer',
  callPrompt: { messages: [systemMsg, userMsg1, userMsg2] },
})
assert.equal(field in systemMsg, false, 'system message must not be tagged')
assert.equal(userMsg1[field], id, 'first user message must be tagged')
assert.equal(field in userMsg2, false, 'second user message must not be tagged after single use')

// 5. Non-user, undefined, and primitive messages pass through unchanged without consuming the identity.
const assistantMsg = { role: 'assistant', content: 'reply' }
const finalUserMsg = { role: 'user', content: 'real-user' }
await session.prompt(prefix2 + id + '\nprimitives', {
  source: 'rpc',
  streamingBehavior: 'steer',
  customRun(s) {
    s.agent.steer(assistantMsg)
    s.agent.steer(undefined)
    s.agent.steer('plain-string')
    s.agent.steer(42)
    s.agent.steer(finalUserMsg)
  },
})
assert.equal(field in assistantMsg, false)
assert.equal(agent1.steered.at(-4).message, undefined)
assert.equal(agent1.steered.at(-3).message, 'plain-string')
assert.equal(agent1.steered.at(-2).message, 42)
assert.equal(finalUserMsg[field], id, 'non-user/primitive messages must not consume the single-use tag')

// 6. Old prefix1 must be rejected after ext2 overwrote existing.config.
const oldPrefixText = prefix1 + id + '\nold-prefix'
const oldPrefixMsg = { role: 'user', content: 'old' }
await session.prompt(oldPrefixText, { source: 'rpc', streamingBehavior: 'steer', callSteer: { message: oldPrefixMsg } })
assert.equal(session.promptCalls.at(-1).text, oldPrefixText, 'old prefix1 must not be stripped after reload')
assert.equal(field in oldPrefixMsg, false, 'old prefix1 must not tag the message')

// 7. Invalid prefix boundaries and wrong source/streamingBehavior must leave text unstripped and message untagged.
const invalidCases = [
  { text: prefix2 + id + 'missing-newline', options: { source: 'rpc', streamingBehavior: 'steer' } },
  { text: prefix2 + 'a'.repeat(47) + '\nshort-id', options: { source: 'rpc', streamingBehavior: 'steer' } },
  { text: prefix2 + 'a'.repeat(49) + '\nlong-id', options: { source: 'rpc', streamingBehavior: 'steer' } },
  { text: prefix2 + '-' + 'a'.repeat(47) + '\nnon-alnum', options: { source: 'rpc', streamingBehavior: 'steer' } },
  { text: prefix2 + id + '\nwrong-source', options: { source: 'ui', streamingBehavior: 'steer' } },
  { text: prefix2 + id + '\nwrong-behavior', options: { source: 'rpc', streamingBehavior: 'followUp' } },
  { text: prefix2 + id + '\nno-behavior', options: { source: 'rpc' } },
]
for (const tc of invalidCases) {
  const msg = { role: 'user', content: tc.text }
  await session.prompt(tc.text, { ...tc.options, callSteer: { message: msg } })
  assert.equal(session.promptCalls.at(-1).text, tc.text, 'invalid steer prefix must not strip text: ' + tc.text)
  assert.equal(field in msg, false, 'invalid steer prefix must not tag message: ' + tc.text)
}

// 8. Replacing session.agent must wrap the replacement agent too.
const agent2 = makeAgent()
session.agent = agent2
const msg2 = { role: 'user', content: 'after-reset' }
await session.prompt(prefix2 + id + '\nafter-reset', { source: 'rpc', streamingBehavior: 'steer', callSteer: { message: msg2 } })
assert.equal(agent2.steered.length, 1)
assert.equal(agent2.steered[0].message[field], id)

// 9. Missing session.agent / agent.steer / agent.prompt throws queue error.
for (const badAgent of [null, { prompt() {} }, { steer() {} }]) {
  const badSession = new AgentSession(badAgent)
  await assert.rejects(
    async () => badSession.prompt(prefix2 + id + '\nbroken', { source: 'rpc', streamingBehavior: 'steer' }),
    /Pi does not expose a queue for steer identity\./,
  )
}

// 10. Nested session.prompt starts a fresh ALS scope; cross-session calls are isolated.
const agentA = makeAgent()
const agentB = makeAgent()
const sessionA = new AgentSession(agentA)
const sessionB = new AgentSession(agentB)
const idA = 'b'.repeat(48)
const idB = 'c'.repeat(48)
// Link sessionB's agent first so its steer wrapper is active.
await sessionB.prompt(prefix2 + idB + '\nprime-b', {
  source: 'rpc',
  streamingBehavior: 'steer',
  callSteer: { message: { role: 'user', content: 'prime' } },
})
const nestedInnerMsg = { role: 'user', content: 'inner' }
const crossSessionMsg = { role: 'user', content: 'cross' }
const outerMsg = { role: 'user', content: 'outer' }
await sessionA.prompt(prefix2 + idA + '\nouter', {
  source: 'rpc',
  streamingBehavior: 'steer',
  async customRun(s) {
    // Nested non-steer prompt must start a fresh scope and not inherit idA.
    await s.prompt('extension-generated', { source: 'extension', callSteer: { message: nestedInnerMsg } })
    // Another session's agent running inside sessionA's async context must not inherit idA.
    sessionB.agent.steer(crossSessionMsg)
    // Outer session still owns idA because neither nested nor cross-session consumed it.
    s.agent.steer(outerMsg)
  },
})
assert.equal(field in nestedInnerMsg, false, 'nested prompt must run in a fresh ALS scope')
assert.equal(field in crossSessionMsg, false, 'cross-session agent must not receive sessionA steer id')
assert.equal(outerMsg[field], idA, 'outer session must keep its unused steer id')
`
	require.NoError(t, os.WriteFile(runner, []byte(script), 0o600))
	cmd := exec.Command(nodePath, runner, path1, path2, cfg1.Prefix, cfg2.Prefix, cfg1.MessageField)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "node verification failed:\n%s", string(out))
}

func TestPiSteerIdentityExtensionSkipsNonFunctionPrompt(t *testing.T) {
	t.Parallel()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "node_modules", "@earendil-works", "pi-coding-agent")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","type":"module","main":"index.mjs"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "index.mjs"), []byte(`export class AgentSession {}`), 0o600))

	bridgeDir := filepath.Join(dir, "bridge")
	require.NoError(t, os.MkdirAll(bridgeDir, 0o755))
	extPath, _, err := writePiSteerBridge(bridgeDir)
	require.NoError(t, err)

	runner := filepath.Join(dir, "verify-non-fn.mjs")
	script := `
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'
import { AgentSession } from '@earendil-works/pi-coding-agent'

const ext = (await import(pathToFileURL(process.argv[2]).href)).default
const cmds = []
AgentSession.prototype.prompt = 'not-a-function'
ext({ registerCommand: (name) => cmds.push(name) })
assert.equal(cmds.length, 0, 'non-function prototype.prompt must skip registration')
assert.equal(AgentSession.prototype.prompt, 'not-a-function')
`
	require.NoError(t, os.WriteFile(runner, []byte(script), 0o600))
	cmd := exec.Command(nodePath, runner, extPath)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "node verification failed:\n%s", string(out))
}
