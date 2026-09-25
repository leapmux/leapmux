import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentProvider as Provider } from '~/generated/proto/leapmux/v1/agent_pb'

/**
 * One saved permission decision, in the two halves the worker stores for it.
 *
 * `request` is the provider section of `supplemental_content`, and `response` is the
 * agent's own bytes from `messages.content`. The worker metadata is deliberately absent:
 * it carries a per-request claim token, which is a credential, and the displayed words
 * never derive from it.
 */
export interface SavedDecisionCapture {
  provider: AgentProvider
  /** A name for the case, used as the test title. */
  name: string
  /** What the transcript row reads, measured in a cold browser. */
  label: string
  request: Record<string, unknown>
  response: Record<string, unknown>
}

/** The hub's own stamps on one captured event: its id, its time, and its sequence number. */
interface ClineHubStamp {
  eventId: string
  timestamp: number
  sequence: number
}

/** The captured approval of a command, which the Allow and the Deny case both answer. */
const CLINE_BASH_APPROVAL: ClineHubStamp = { eventId: 'hevt_1790258525021_pu703', timestamp: 1790258525021, sequence: 324 }

/** One event envelope of Cline's hub, as the capture recorded it. */
function clineHubEvent(event: string, stamp: ClineHubStamp, payload: Record<string, unknown>): Record<string, unknown> {
  return { version: 'v1', event, eventId: stamp.eventId, sessionId: '1790258346189_zqp76', timestamp: stamp.timestamp, payload, sequence: stamp.sequence }
}

/**
 * Saved decisions CAPTURED from the installed runtimes, rather than written by hand
 * (RL-001): five on September 14, 2026, Kimi Code, Grok Build, Qwen Code and Oh My
 * Pi on September 23, and Codewhale, Kiro and Cline on September 24.
 *
 * The five of the first capture are here because theirs were the runtimes that asked
 * for permission on that day. Claude Code, Cursor, OpenCode, Kilo and Pi approved the
 * same operations on their own: the first two have a mode that asks and did not use it
 * for a write inside their own working directory, and the last three expose no
 * permission control at all.
 *
 * Each entry pins one native wire shape to the words the reader sees. A runtime that
 * changes that shape fails the corpus test instead of quietly degrading a reloaded row to
 * the generic "Responded".
 */
export const SAVED_DECISION_CORPUS: readonly SavedDecisionCapture[] = [
  // Codex answers with a bare decision word and states no option list.
  {
    provider: Provider.CODEX,
    name: 'codex Allow',
    label: 'Allow',
    request: {
      method: 'item/fileChange/requestApproval',
      id: 0,
      params: {
        threadId: '01a09eee-c9e5-7af0-821f-8263b3228ae8',
        turnId: '01a09f1a-6b3f-7863-857c-1983b4eaf0ef',
        itemId: 'exec-bdce7ddf-fe2f-4f8b-a807-c2a038d43e5c',
        startedAtMs: 1789375706740,
        reason: null,
        grantRoot: null,
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 0,
      result: {
        decision: 'accept',
      },
    },
  },
  // Copilot names its own approval kind, and `approve-once` is the single-use one.
  {
    provider: Provider.GITHUB_COPILOT,
    name: 'github-copilot Allow once',
    label: 'Allow once',
    request: {
      jsonrpc: '2.0',
      method: 'session.event',
      params: {
        sessionId: '4d29e3a0-ea62-4580-8080-55c32e89058c',
        event: {
          type: 'permission.requested',
          data: {
            requestId: 'da1d347c-00fa-4b9a-ad64-874f9c2bb5a3',
            permissionRequest: {
              kind: 'shell',
              toolCallId: 'call_9s8QPL2WSVY7xwYumO7hAgOX',
              fullCommandText: 'ls -1',
              intention: 'List current directory entries',
              commands: [
                {
                  identifier: 'ls -1',
                  readOnly: false,
                },
              ],
              commandSegments: [
                {
                  identifier: 'ls',
                  fullCommandText: 'ls -1',
                },
              ],
              possiblePaths: [],
              possibleUrls: [],
              hasWriteFileRedirection: false,
              canOfferSessionApproval: false,
            },
            promptRequest: {
              kind: 'commands',
              fullCommandText: 'ls -1',
              intention: 'List current directory entries',
              commandIdentifiers: [
                'ls -1',
              ],
              canOfferSessionApproval: false,
              toolCallId: 'call_9s8QPL2WSVY7xwYumO7hAgOX',
              assistedApproval: {
                recommendation: 'approve',
                reason: 'User explicitly requested listing current directory entries with `ls -1`; it is harmless read-only inspection.',
                model: 'gpt-5.6-luna',
              },
            },
            agentMode: 'interactive',
          },
          id: '6be5cc1f-e115-4f72-b624-ad288cbd1b9f',
          timestamp: '2026-09-14T08:02:41.484Z',
          parentId: '394bc080-5aee-469e-ac1d-c1c7a0ce3b12',
        },
      },
    },
    response: {
      response: {
        request_id: 'copilot-HPfsGc_Bya70ZnVHt8Ac0GUO4VJoSaaMjmUCGqE6RlU',
        response: {
          kind: 'approve-once',
        },
        subtype: 'success',
      },
      type: 'control_response',
    },
  },
  // The location scope carries a second `approval.kind` beside the decision.
  {
    provider: Provider.GITHUB_COPILOT,
    name: 'github-copilot Allow for this project',
    label: 'Allow for this project',
    request: {
      jsonrpc: '2.0',
      method: 'session.event',
      params: {
        sessionId: '4d29e3a0-ea62-4580-8080-55c32e89058c',
        event: {
          type: 'permission.requested',
          data: {
            requestId: '09aef253-4031-438c-b31f-abce2bc8f8d3',
            permissionRequest: {
              kind: 'write',
              toolCallId: 'custom_call_XHRLjJLbxJTEhzZX19TApHPv',
              intention: 'Create file',
              fileName: '/work/probe-two.txt',
              diff: '\ndiff --git a/work/probe-two.txt b/work/probe-two.txt\ncreate file mode 100644\nindex 0000000..0000000\n--- a/dev/null\n+++ b/work/probe-two.txt\n@@ -1,0 +1,2 @@\n+beta\n+\n\n',
              newFileContents: 'beta\n',
              canOfferSessionApproval: true,
            },
            promptRequest: {
              kind: 'write',
              toolCallId: 'custom_call_XHRLjJLbxJTEhzZX19TApHPv',
              intention: 'Create file',
              fileName: '/work/probe-two.txt',
              diff: '\ndiff --git a/work/probe-two.txt b/work/probe-two.txt\ncreate file mode 100644\nindex 0000000..0000000\n--- a/dev/null\n+++ b/work/probe-two.txt\n@@ -1,0 +1,2 @@\n+beta\n+\n\n',
              newFileContents: 'beta\n',
              canOfferSessionApproval: true,
              assistedApproval: {
                recommendation: 'requireApproval',
                reason: 'Proposed file name and content differ from the user\'s authorized request.',
                model: 'gpt-5.6-luna',
              },
            },
            agentMode: 'interactive',
          },
          id: 'ea864f5f-0c4f-463f-8e2c-9e2064ad7d0e',
          timestamp: '2026-09-14T08:12:24.993Z',
          parentId: '7774c31d-8b92-4134-a4ce-3635720b7d92',
        },
      },
    },
    response: {
      response: {
        request_id: 'copilot-ySEH4ZYy1nWBJijF3XCHFJ0jQM2Jm3AI4785BVe0lJg',
        response: {
          approval: {
            kind: 'write',
          },
          kind: 'approve-for-location',
        },
        subtype: 'success',
      },
      type: 'control_response',
    },
  },
  // Goose names every option after its own id, so the words come from the option `kind` (ACP-004).
  {
    provider: Provider.GOOSE,
    name: 'goose Allow once',
    label: 'Allow once',
    request: {
      jsonrpc: '2.0',
      id: '9f6040cd-a7e4-49bf-b497-f73c73b09d3f',
      method: 'session/request_permission',
      params: {
        sessionId: '20260914_21',
        toolCall: {
          toolCallId: 'call_400f32a30c3c49708c3d1995',
          kind: 'other',
          status: 'pending',
          title: 'todo: todo write',
          rawInput: {
            content: '- [ ] Run `ls -1` in the current directory via the shell tool\n- [ ] Report its output to the user',
          },
        },
        options: [
          {
            optionId: 'allow_always',
            name: 'allow_always',
            kind: 'allow_always',
          },
          {
            optionId: 'allow_once',
            name: 'allow_once',
            kind: 'allow_once',
          },
          {
            optionId: 'reject_once',
            name: 'reject_once',
            kind: 'reject_once',
          },
          {
            optionId: 'reject_always',
            name: 'reject_always',
            kind: 'reject_always',
          },
        ],
      },
    },
    response: {
      jsonrpc: '2.0',
      id: '9f6040cd-a7e4-49bf-b497-f73c73b09d3f',
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'allow_once',
        },
      },
    },
  },
  // Reasonix writes its own option names, and its single-use one is a bare "Allow".
  {
    provider: Provider.REASONIX,
    name: 'reasonix Allow',
    label: 'Allow',
    request: {
      jsonrpc: '2.0',
      id: 1,
      method: 'session/request_permission',
      params: {
        sessionId: '1901ef1a-52bf-4fe8-974a-5b36c29ef6ef',
        toolCall: {
          toolCallId: 'gate-1',
          title: 'write_file reload-probe.txt',
          kind: 'edit',
          status: 'pending',
          rawInput: {
            content: 'alpha',
            path: 'reload-probe.txt',
          },
          locations: [
            {
              path: '/work/reload-probe.txt',
            },
          ],
          _meta: {
            'reasonix.io': {
              approvalId: '1',
              fresh: false,
              subject: 'reload-probe.txt',
              tool: 'write_file',
            },
          },
        },
        options: [
          {
            optionId: 'allow_once',
            name: 'Allow',
            kind: 'allow_once',
          },
          {
            optionId: 'allow_always',
            name: 'Allow Edit for this session',
            kind: 'allow_always',
          },
          {
            optionId: 'reject_once',
            name: 'Reject',
            kind: 'reject_once',
          },
        ],
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 1,
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'allow_once',
        },
      },
    },
  },
  {
    provider: Provider.REASONIX,
    name: 'reasonix Allow Bash=ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt for this session',
    label: 'Allow Bash=ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt for this session',
    request: {
      jsonrpc: '2.0',
      id: 5,
      method: 'session/request_permission',
      params: {
        sessionId: '1901ef1a-52bf-4fe8-974a-5b36c29ef6ef',
        toolCall: {
          toolCallId: 'gate-5',
          title: 'bash ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt',
          kind: 'execute',
          status: 'pending',
          rawInput: {
            command: 'ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt',
          },
          _meta: {
            'reasonix.io': {
              approvalId: '5',
              fresh: false,
              subject: 'ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt',
              tool: 'bash',
            },
          },
        },
        options: [
          {
            optionId: 'allow_once',
            name: 'Allow',
            kind: 'allow_once',
          },
          {
            optionId: 'allow_always',
            name: 'Allow Bash=ls -1; echo \'---\'; wc -c probe-two.txt; od -c probe-two.txt for this session',
            kind: 'allow_always',
          },
          {
            optionId: 'reject_once',
            name: 'Reject',
            kind: 'reject_once',
          },
        ],
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 5,
      result: {
        outcome: {
          outcome: 'selected',
          optionId: 'allow_always',
        },
      },
    },
  },
  // ZCode answers with a decision word and a reason, and offers no approval scope.
  {
    provider: Provider.ZCODE,
    name: 'zcode Allow',
    label: 'Allow',
    request: {
      type: 'control_request',
      request_id: 'perm_0aa4972e-11ff-468d-9795-014ea21b46e8',
      id: 'server-3',
      method: 'interaction/requestPermission',
      request: {
        tool_name: 'Write',
        tool_use_id: 'call_a34f7e8c209048e88a6768ae',
        input: {
          file_path: '/work/reload-probe.txt',
          content: 'alpha',
        },
      },
      params: {
        input: {
          file_path: '/work/reload-probe.txt',
          content: 'alpha',
        },
        reason: 'Tool has side effects and requires approval',
        requestId: 'perm_0aa4972e-11ff-468d-9795-014ea21b46e8',
        riskLevel: 'medium',
        sessionId: 'sess_7967eb7a-1f1c-4df1-8e2a-0f53459d3c77',
        options: [
          {
            kind: 'allow_once',
            name: 'Allow once',
            optionId: 'allow_once',
            response: {
              decision: 'allow',
              reason: 'Approved once',
            },
          },
          {
            description: 'Do not ask again for matching requests in this project',
            kind: 'allow_always',
            name: 'Always allow in this project',
            optionId: 'allow_project',
            response: {
              decision: 'allow',
              permissionUpdates: [
                {
                  behavior: 'allow',
                  rules: [
                    {
                      toolName: 'Write',
                      ruleContent: '/work/reload-probe.txt',
                    },
                  ],
                  type: 'addRules',
                },
              ],
              reason: 'Approved for this project',
            },
          },
          {
            kind: 'deny',
            name: 'Deny',
            optionId: 'deny',
            response: {
              decision: 'deny',
              reason: 'Denied',
            },
          },
        ],
        toolCallId: 'call_a34f7e8c209048e88a6768ae',
        toolName: 'Write',
        turnId: 'turn_f78e2d5b-99be-415b-82e1-ff91e03f30ad',
      },
    },
    response: {
      id: 'server-3',
      result: {
        decision: 'allow',
        reason: 'Approved once',
      },
    },
  },
  // Codewhale answers an approval with a decision word alone. The response is the reply
  // frame the worker posted, and the request is the stored payload beside the runtime's
  // own `approval.required` event, from Codewhale 0.9.13.
  {
    provider: Provider.CODEWHALE,
    name: 'codewhale Allow',
    label: 'Allow',
    request: {
      event: {
        schema_version: 1,
        seq: 17,
        event: 'approval.required',
        kind: 'approval.required',
        thread_id: 'thr_700b0f43',
        turn_id: 'turn_94101b47',
        item_id: null,
        timestamp: '2026-09-23T20:05:41.351736+00:00',
        created_at: '2026-09-23T20:05:41.351736+00:00',
        payload: {
          id: 'approval_14c379cb62564e9db7baae791c12a0fe',
          approval_id: 'approval_14c379cb62564e9db7baae791c12a0fe',
          tool_call_id: 'call_f984d324',
          tool_name: 'bash',
          description: 'Execute a shell command in the workspace and return stdout and stderr. Output keeps the last 2000 lines or 50KB. An optional timeout is expressed in seconds; when omitted the command is killed after 120 seconds, so pass an explicit timeout for work expected to take longer. In Ask, after a sandbox denial, retry the exact command once with sandbox_permissions (the narrowest wider mode that suffices) and a one-sentence justification; the approval prompt asks the user.',
          intent_summary: null,
        },
        previous_seq: 16,
      },
      request: {
        tool_name: 'bash',
        tool_use_id: 'call_f984d324',
        input: {
          command: 'touch x.txt',
        },
      },
      request_id: 'approval:approval_14c379cb62564e9db7baae791c12a0fe',
      type: 'control_request',
    },
    response: {
      frame: 'approval',
      approval_id: 'approval_14c379cb62564e9db7baae791c12a0fe',
      decision: 'allow',
    },
  },
  // Kimi Code states its request as an `event.approval.requested`, which the worker
  // stores verbatim (captured from a live Kimi Code 2.0.2 session, September 23, 2026).
  // The stored answer is the server's own approval body in the envelope the worker
  // writes: the body that the capture posted, with the session scope that an approval
  // for the session adds.
  {
    provider: Provider.KIMI_CODE,
    name: 'kimi Allow for this session',
    label: 'Allow for this session',
    request: {
      type: 'event.approval.requested',
      agentId: 'main',
      sessionId: 'session_f7cf22a1-3d27-4d41-b8e2-978b1e5fa5a7',
      approval_id: 'approval_bd6a909a-e27b-4e67-9ccf-8812f349173e',
      session_id: 'session_f7cf22a1-3d27-4d41-b8e2-978b1e5fa5a7',
      agent_id: 'main',
      turn_id: 0,
      tool_call_id: 'call_bash_1',
      tool_name: 'Bash',
      action: 'Running: echo hi-from-bash',
      tool_input_display: {
        kind: 'command',
        command: 'echo hi-from-bash',
        cwd: '/work',
        description: 'Echo a greeting',
        language: 'bash',
      },
      created_at: '2026-09-23T18:06:49.141Z',
      expires_at: '2026-09-24T18:06:49.141Z',
    },
    response: {
      type: 'control_response',
      response: {
        subtype: 'success',
        request_id: 'approval_bd6a909a-e27b-4e67-9ccf-8812f349173e',
        response: {
          decision: 'approved',
          scope: 'session',
        },
      },
    },
  },
  // Grok Build and Qwen Code, captured from their live ACP sessions on September 23,
  // 2026 (the permission request from the wire, and the reply LeapMux sends for the
  // option the reader chose). Both write their own option names.
  {
    provider: Provider.GROK_BUILD,
    name: 'grok Yes, proceed',
    label: 'Yes, proceed',
    request: {
      jsonrpc: '2.0',
      id: 0,
      method: 'session/request_permission',
      params: {
        sessionId: '01a0cf76-dd5f-7dd0-b6a3-934dd82157cf',
        toolCall: {
          toolCallId: 'call_2_0',
          kind: 'execute',
          title: 'Execute `echo probe > probe.txt && ls`',
          rawInput: { variant: 'Bash', command: 'echo probe > probe.txt && ls', description: 'Write probe file', is_background: false },
          _meta: { 'x.ai/tool': { version: 1, name: 'run_terminal_command', kind: 'execute', namespace: 'grok_build', label: 'Run Command', read_only: false, input: { command: 'echo probe > probe.txt && ls', description: 'Write probe file' } } },
        },
        options: [
          { optionId: 'always-allow', name: 'Yes, and don\'t ask again for bash commands', kind: 'allow_always' },
          { optionId: 'allow-once', name: 'Yes, proceed', kind: 'allow_once' },
          { optionId: 'reject-once', name: 'No, and tell Grok what to do differently', kind: 'reject_once' },
          { optionId: 'reject-always', name: 'No, and don\'t ask again for this command', kind: 'reject_always' },
        ],
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 0,
      result: { outcome: { outcome: 'selected', optionId: 'allow-once' } },
    },
  },
  // Kiro, captured from a v3 `kiro-cli-chat acp` session on September 24, 2026.
  {
    provider: Provider.KIRO,
    name: 'kiro Allow',
    label: 'Allow',
    request: {
      jsonrpc: '2.0',
      id: 3,
      method: 'session/request_permission',
      params: {
        sessionId: 'sess_ca621409-cf06-4c03-8911-d4c820271708',
        toolCall: { toolCallId: 'run_command_t_sh', status: 'pending', title: 'echo v3-shell' },
        options: [
          { optionId: 'accept', name: 'Allow', kind: 'allow_once' },
          { optionId: 'always-accept', name: 'Always allow', kind: 'allow_always' },
          { optionId: 'reject', name: 'Deny', kind: 'reject_once' },
          { optionId: 'always-reject', name: 'Always deny', kind: 'reject_always' },
        ],
        _meta: { kiro: { toolId: 'run_command', command: 'echo v3-shell', consent: { capability: 'shell', resource: 'echo v3-shell', askType: 'implicit', workspaceRoot: '/w' }, consentRound: 1 } },
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 3,
      result: { outcome: { outcome: 'selected', optionId: 'accept' } },
    },
  },
  {
    provider: Provider.QWEN_CODE,
    name: 'qwen Allow',
    label: 'Allow',
    request: {
      jsonrpc: '2.0',
      id: 0,
      method: 'session/request_permission',
      params: {
        sessionId: '70dab2cd-62c7-4f76-b53e-50a950df1520',
        options: [
          { optionId: 'proceed_always_project', name: 'Always Allow in project: touch *', kind: 'allow_always' },
          { optionId: 'proceed_always_user', name: 'Always Allow for user: touch *', kind: 'allow_always' },
          { optionId: 'proceed_once', name: 'Allow', kind: 'allow_once' },
          { optionId: 'cancel', name: 'Reject', kind: 'reject_once' },
        ],
        toolCall: {
          toolCallId: 'call_a0915b2ff3',
          status: 'pending',
          title: 'touch /work/touched.txt (Create a marker file)',
          content: [],
          locations: [],
          kind: 'execute',
          rawInput: { command: 'touch /work/touched.txt', description: 'Create a marker file' },
          _meta: { toolName: 'run_shell_command' },
        },
      },
    },
    response: {
      jsonrpc: '2.0',
      id: 0,
      result: { outcome: { outcome: 'selected', optionId: 'proceed_once' } },
    },
  },
  // Oh My Pi asks with a `select` dialog and takes its own option word as the answer.
  // Captured from omp 18.2.11 on September 23, 2026, with `tools.approvalMode:
  // always-ask`; the answers are the extension_ui_response lines the browser sends.
  {
    provider: Provider.OH_MY_PI,
    name: 'oh my pi Approve',
    label: 'Allow',
    request: {
      type: 'extension_ui_request',
      id: '158b2ba5001bfb93',
      method: 'select',
      title: 'Allow tool: bash\nCommand: echo approved-run',
      options: ['Approve', 'Deny'],
    },
    response: {
      type: 'extension_ui_response',
      id: '158b2ba5001bfb93',
      value: 'Approve',
    },
  },
  {
    provider: Provider.OH_MY_PI,
    name: 'oh my pi Deny',
    label: 'Deny',
    request: {
      type: 'extension_ui_request',
      id: '158b2ba55c9bfb95',
      method: 'select',
      title: 'Allow tool: bash\nCommand: echo denied-run',
      options: ['Approve', 'Deny'],
    },
    response: {
      type: 'extension_ui_response',
      id: '158b2ba55c9bfb95',
      value: 'Deny',
    },
  },
  // Cline states each approval and each question as its hub's own event, which the worker
  // stores verbatim: the requests below were captured from the Cline 3.0.64 hub on
  // September 24, 2026. The stored answer is the reply the worker sends Cline, as
  // Cline's `approval.respond` and `capability.respond` take it. The worker keeps the
  // mode an approved plan switches to in the stored copy alone.
  {
    provider: Provider.CLINE,
    name: 'cline Allow',
    label: 'Allow',
    request: clineHubEvent('approval.requested', CLINE_BASH_APPROVAL, {
      approvalId: 'approval_1790258525021_0l541',
      sessionId: '1790258346189_zqp76',
      agentId: 'agent_1790258346331_0r5l6z',
      conversationId: 'conv_1790258346406_9ncl4sl',
      iteration: 1,
      toolCallId: 'call_bash_1',
      toolName: 'run_commands',
      inputJson: '{"commands":["echo probe-bash"]}',
      policy: { autoApprove: false },
    }),
    response: { approvalId: 'approval_1790258525021_0l541', approved: true },
  },
  {
    provider: Provider.CLINE,
    name: 'cline Deny',
    label: 'Deny',
    request: clineHubEvent('approval.requested', CLINE_BASH_APPROVAL, {
      approvalId: 'approval_1790258525021_0l541',
      sessionId: '1790258346189_zqp76',
      agentId: 'agent_1790258346331_0r5l6z',
      conversationId: 'conv_1790258346406_9ncl4sl',
      iteration: 1,
      toolCallId: 'call_bash_1',
      toolName: 'run_commands',
      inputJson: '{"commands":["echo probe-bash"]}',
      policy: { autoApprove: false },
    }),
    response: { approvalId: 'approval_1790258525021_0l541', approved: false, reason: 'The user declined this tool call.' },
  },
  {
    provider: Provider.CLINE,
    name: 'cline plan Approve',
    label: 'Approve (Act)',
    request: clineHubEvent('approval.requested', { eventId: 'hevt_1790258352751_h0q45', timestamp: 1790258352751, sequence: 286 }, {
      approvalId: 'approval_1790258352751_4cqa2',
      sessionId: '1790258346189_zqp76',
      agentId: 'agent_1790258346331_0r5l6z',
      conversationId: 'conv_1790258346406_9ncl4sl',
      iteration: 1,
      toolCallId: 'call_switch_1',
      toolName: 'switch_to_act_mode',
      inputJson: '{}',
      policy: { autoApprove: false },
    }),
    response: { approvalId: 'approval_1790258352751_4cqa2', approved: true, permissionMode: 'act' },
  },
  {
    provider: Provider.CLINE,
    name: 'cline question answer',
    label: 'Blue',
    request: clineHubEvent('capability.requested', { eventId: 'hevt_1790258346728_xavjj', timestamp: 1790258346728, sequence: 133 }, {
      requestId: 'capreq_1790258346728_1gzza',
      targetClientId: 'client_leapmux_probe_73642',
      capabilityName: 'tool_executor.askQuestion',
      payload: {
        executor: 'askQuestion',
        args: ['Which color do you prefer?', ['Red', 'Blue']],
        context: {
          sessionId: '1790258346189_zqp76',
          agentId: 'agent_1790258346331_0r5l6z',
          conversationId: 'conv_1790258346406_9ncl4sl',
          runId: 'run_N9WC0hrX',
          iteration: 1,
          toolCallId: 'call_ask_1',
          metadata: { modelSupportsImages: true },
        },
      },
    }),
    response: { requestId: 'capreq_1790258346728_1gzza', ok: true, payload: { result: 'Blue' } },
  },
]
