import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

/**
 * The receipt runtime that a private MCP server imports when it writes a receipt. It writes one receipt format, which
 * `./mcpServerReceipt.ts` parses:
 * - The protocol events that a proof reads: the initialize capabilities, the tool catalogs, the input requests and
 *   their replies, and the tool results.
 * - The raw exchange, so a proof can read exact call arguments and the exact reply to a call. It holds each message
 *   that the stdio runtime passes to the script, and each message that the script sends, in order. The stdio runtime
 *   drops a notification and answers an invalid envelope itself, so neither appears.
 */
const runtimeScript = `
import { renameSync, writeFileSync } from 'node:fs';
export function writeMcpReceiptValue(receiptLog, value) {
  if (!receiptLog) return;
  const sibling = receiptLog + '.writing-' + process.pid;
  writeFileSync(sibling, JSON.stringify(value));
  renameSync(sibling, receiptLog);
}
export function createMcpServerReceipt(receiptLog = null) {
  const receipt = { initializeCapabilities: null, toolCatalogs: [], elicitationRequests: [], elicitationReplies: [], toolResults: [], exchange: [] };
  const save = () => writeMcpReceiptValue(receiptLog, receipt);
  save();
  return {
    initialized(capabilities) { receipt.initializeCapabilities = capabilities; save(); },
    listed(id, tools) { receipt.toolCatalogs.push({ id, tools: tools.map(({ name, inputSchema }) => ({ name, inputSchema })) }); save(); },
    requested(id, toolRequestId, params) { receipt.elicitationRequests.push({ id, toolRequestId, params }); save(); },
    replied(reply) { receipt.elicitationReplies.push(reply); save(); },
    completed(id, tool, text, isError = false) { receipt.toolResults.push({ id, tool, text, isError }); save(); },
    received(message) { receipt.exchange.push({ received: message }); save(); },
    sent(message) { receipt.exchange.push({ sent: message }); save(); },
  };
}
`

/** Write the shared receipt runtime beside each private native MCP server. */
export function writeMcpReceiptRuntime(directory: string): string {
  const path = join(directory, 'mcp-receipt-runtime.mjs')
  writeFileSync(path, runtimeScript)
  return path
}
