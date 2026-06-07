---
title: "Collaboration & Presence"
type: docs
weight: 13
---

LeapMux lets several people — and several of your own devices and browser tabs — work in the same workspace at the same time. What stays in sync is the **workspace layout**: the tiling tree, tabs, splits, grids, and floating windows. This chapter explains exactly what syncs, how conflicts are resolved, who can see what, and the one real presence signal LeapMux exposes.

> **Note:** LeapMux collaboration is **shared-layout sync**, not document or text co-editing. There is no co-typing in an agent chat or terminal, no shared text cursor, and no "Google Docs"-style multi-caret editing. Two collaborators share the *structure* of a workspace; the agent and terminal content each user sees is decrypted on their own machine over their own end-to-end-encrypted channel (see [Security & Threat Model](/docs/23-security-and-threat-model/)).

## What syncs live

When two or more clients have the same workspace open, the following stay in sync in near-real-time. When one participant changes any of them, the others see the change appear, move, or disappear within roughly a frame to a network round-trip:

- **The tiling tree** — split panes, grids, and leaf tiles. Splitting a pane, changing a split direction, or dragging a divider to resize propagates to everyone.
- **Tabs** — agent, terminal, and file tabs: their existence, their order within a tile, which tile they live in, and which Worker hosts them. Opening, closing, moving, and reordering tabs all sync.
- **Floating windows** — their position, width, height, opacity, and the tile tree inside them.
- **Workspace lifecycle** — when a workspace is created, renamed, or deleted, the sidebar updates on every connected client.
- **Tab renames and file-tab paths** — a tab renamed by one client, and the file path of a file tab opened by one client, show up for the others.

This is the substance of "two people in the same workspace": one user opens an agent, splits the layout, pops a tile out into a floating window, or closes a tab, and the other user sees it happen.

For the layout primitives themselves — tiles, splits, grids, floating windows, and how you manipulate them — see [Tabs & Layout](/docs/08-tabs-and-layout/). For workspace creation, renaming, deletion, and sharing, see [Workspaces](/docs/07-workspaces/).

### What does NOT sync

To set expectations precisely, the following are **not** part of LeapMux collaboration:

- No live text co-editing of an agent prompt, chat, or terminal.
- No shared text cursor, caret, or selection.
- No co-typing — each user types into their own client.

And the following social-presence features simply do not exist in LeapMux:

- No collaborator avatars or "presence faces".
- No "X is viewing this workspace" labels or badges.
- No remote cursors or pointer tracking.
- No per-user color assignment or identity chips.
- No typing indicators.

The only presence signal that exists is the per-workspace **active-client** signal, described in [The one presence signal: active client](#the-one-presence-signal-active-client) below.

## How live sync works (the short version)

Layout, tabs, and floating windows are modeled as a **CRDT** (a conflict-free replicated data type): a set of registers and tombstones that every client folds into the same shared state. Each client connects to the Hub over a single per-organization WebSocket and receives a stream of events; the first event is always a full snapshot of everything the client is allowed to see, followed by incremental updates.

**Layout syncs through the Hub; content stays on the Workers:**

```text
                   Hub (127.0.0.1:4327)
        ┌─────────────────────────────────────────┐
        │ Layout CRDT  -  last-write-wins by HLC  │
        │ /ws/orgevents:  snapshot + live deltas  │
        └───┬───────────────────────────┬─────────┘
            ▲   layout sync (CRDT)      ▲
            │   tabs, splits,           │
            │   floats                  │
   ┌────────┴────────┐         ┌────────┴────────┐
   │     Client A    │         │     Client B    │
   │  (browser tab)  │         │  (other device) │
   └────────┬────────┘         └────────┬────────┘
            │   E2EE content            │
            │   (Hub cannot read)       │
            ▼                           ▼
        ┌───┴───────────────────────────┴─────────┐
        │ Workers  -  agent / terminal / file     │
        │ tab content (decrypted per client)      │
        └─────────────────────────────────────────┘
```

Your own actions apply **optimistically** — they show on screen instantly, before the Hub has confirmed them — and then reconcile when the Hub's echo arrives. You do not wait for a server round-trip to see your own tab open.

> **Note:** You never configure or manage this sync. There is no "start collaborating" button, no session link to share, and no sync toggle. Live sync is always on for any workspace you can see; opening the same workspace from two places is all it takes.

### Concurrent edits resolve automatically

When two participants change the same thing at the same time — for example, both drag the same tab to different tiles — the conflict is resolved deterministically with **last-write-wins by a Hybrid Logical Clock (HLC)**. The Hub assigns the authoritative clock value to every operation as it commits, so:

- There is **no merge dialog** and **no error** shown to either user.
- The losing edit is silently superseded by the winning one.
- The outcome is **identical on every client** — everyone converges on the same layout.

Because creation links (a tile's parent, a floating window's root) are set once and never reassigned, concurrent edits can never strand a tab or tile in a broken state. The projection that turns the shared CRDT state into what you see also repairs anomalies the same way on every client: tombstoned items are skipped, orphaned nodes are dropped, a split with a single remaining child collapses to that child, and malformed grids fall back to sensible defaults. The net effect for you as a user is simple: **no orphaned tabs, no divergent layouts.**

### When an item leaves your view

Collaboration is scoped to what you can see (see [Visibility is permission-scoped](#visibility-is-permission-scoped)). If something you were editing moves out of your visible set — for instance, a tab is moved into a workspace you do not have access to — your in-flight change to it is discarded and you see a warning toast:

> A pending change was discarded because the affected item left your view.

This is expected, not an error: the item is simply no longer yours to edit.

## Syncing across your own devices and tabs

Live sync is not just for multiple people. It applies just as much to **your own** multiple browser tabs, windows, and devices:

- Open the same workspace in two browser tabs, and a split or tab you create in one appears in the other.
- Open it on your laptop and your phone, and the layouts stay in step.

> **Note:** Layout sync is per-tab — two tabs of the same browser each render the live layout independently. But for the **active-client** signal below, presence identity is tied to your authenticated *session*, not to a tab. Because every tab and window of the same browser profile shares one session cookie, they all count as a **single** presence client. A distinct presence client is a distinct session: a separate browser or browser profile (separate cookie jar), a different device, or a CLI bearer token.

For desktop vs. mobile layout differences, see [Tabs & Layout](/docs/08-tabs-and-layout/).

## The one presence signal: active client

LeapMux tracks exactly one presence fact per workspace: which connected client is the **active client** — the one whose keyboard, pointer, or scroll input (or tab-becoming-visible event) is most recent. This is the only presence primitive in the product.

### Its only effect: who hears the turn-end sound

The active-client signal has a single user-visible job: **gating the agent turn-end notification sound** so that when an agent finishes a turn, only the *focused* client dings — not every open tab, window, and device at once.

The behavior:

- The client that is currently active for the active workspace plays the sound.
- Other clients viewing the same workspace **stay silent**.
- If LeapMux can't determine a clear active client yet (for example, no input has been registered, or two clients tie on the most-recent input), it errs on the side of playing the sound rather than swallowing the notification for a focused user.

Because presence is keyed to your session — not to individual tabs — all tabs and windows of the *same* browser are one client. They will not compete for the ding among themselves: whichever of them most recently had your input is the one client, and it dings while a *different* session (another browser, device, or CLI token) viewing the same workspace stays silent.

> **Tip:** The turn-end sound also respects your own preference. In the **Preferences** dialog (see [Settings & Preferences](/docs/14-settings-and-preferences/)) — under the **This Browser** and **Account Defaults** tabs — the **Turn End Sound** section offers **None**, **Ding Dong**, and **Use account default**, plus a **Volume** control. If you set it to **None**, that client never dings regardless of whether it is the active client.

There are additional conditions on the sound that are unrelated to presence: trivial single-exchange turns (an agent reply with no tool use) do not ding, the sound is rate-limited so it can't fire repeatedly in quick succession, and it is suppressed for an agent that is in the middle of closing.

### How the active client is decided

You do not set the active client manually — it follows your input automatically:

- Typing, clicking, or scrolling marks your client as active for the workspace you are looking at. Switching back to a tab (making it visible again) does so immediately.
- There is **no inactivity timeout** while you stay connected. An idle-but-still-connected client remains the active client without you having to touch anything; LeapMux holds your presence for as long as your connection is open.
- Brief disconnects do not flicker the signal: if your client reconnects within a short grace window (60 seconds), its active status is preserved, so a momentary network blip won't hand the ding to another session.

> **Note:** Presence identity is derived by the Hub from your authenticated session, not claimed by the client — the heartbeat your client sends carries no client identifier, so a client cannot spoof being "active" on another's behalf. This is also why all tabs of one browser collapse to a single presence client: they share one session cookie, so the Hub sees one identity. Two *different* sessions of the same user — say, two separate browser profiles, each with its own cookie jar — are treated as two distinct clients.

## Visibility is permission-scoped

You only ever see — and only ever sync with — the workspaces you are allowed to read. The live event stream is filtered per user against the workspace access rules:

- A workspace you **own** is always visible to you.
- A workspace someone else owns is visible to you only if its owner has explicitly **shared** it with you (granted you access). Org membership alone grants nothing — being in the same organization does not let you see another member's workspaces.

When a workspace is newly shared with you, or a tab moves into a workspace you can read, it materializes in your view automatically — you do not have to refresh. Conversely, when something leaves your visible set, it disappears (and any pending edit you had on it is dropped, with the toast shown earlier).

For how to grant and revoke access, and the important detail that **sharing grants routing permission only** — the recipient must still open their own end-to-end-encrypted channel to the Worker to actually read agent/terminal content — see [Workspaces](/docs/07-workspaces/). For roles (Owner / Admin / Member) and how org membership relates to workspace access, see [Organizations & Members](/docs/06-organizations-and-members/).

> **Warning:** "All org members" sharing is offered in the sharing dialog but is **not currently functional** — the backend rejects it. To collaborate with specific people today, share with **Specific members**. See [Workspaces](/docs/07-workspaces/) for details.

## Security note

The live-sync channel carries workspace *structure* — tab positions, tiling geometry, and workspace titles — which the Hub routes and can see, since it relays these events. The Hub **cannot** see agent transcripts, tool calls, terminal I/O, file contents, or Worker paths: that content travels over a separate end-to-end-encrypted channel between each client and the Worker. Collaboration therefore shares layout through the Hub while keeping the actual work content end-to-end encrypted per participant. See [Security & Threat Model](/docs/23-security-and-threat-model/) and [Encryption & Data](/docs/22-encryption-and-data/) for the full model.

## Solo mode

In **solo mode** the Hub, Worker, and your single client run together on your own machine, so "collaboration" reduces to syncing your own browser tabs and windows against the same local instance — layout sync and the active-client ding still work exactly as described. Workspace sharing is disabled in solo mode (the sharing controls are hidden), because there are no other users to share with. For run modes, see [Running LeapMux](/docs/17-running-leapmux/).

## Troubleshooting

| Symptom | Likely cause | What to do |
| --- | --- | --- |
| A change I made in one tab didn't appear in another | The two tabs are not on the same workspace, or one lost its connection | Confirm both are open on the same workspace; reload the tab that's behind to re-sync. |
| Every session/device dinged when an agent finished | No clear active client could be determined (degraded fallback), or each was a *separate* session (different browser/device/CLI token) | This is intentional in the degraded case; interact with the session you want to be active, or set **Turn End Sound** to **None** on the others. (Multiple tabs of one browser share a session and won't each ding.) |
| No session dinged at all | **Turn End Sound** is set to **None**, or the turn was trivial / rate-limited | Set **Turn End Sound** to **Ding Dong** in the **Preferences** dialog; note trivial turns and rapid repeats are intentionally silenced. |
| A layout change I dragged "snapped back" | A concurrent edit by another client won the last-write-wins resolution | Re-apply the change; whoever writes last wins, and the result is consistent for everyone. |
| "A pending change was discarded…" toast | The item you were editing left your visible set (e.g. moved to a workspace you can't read) | Expected behavior — the item is no longer accessible to you. |
| A collaborator can't see a workspace I shared | "All org members" was used (non-functional), or no explicit grant exists | Re-share using **Specific members**; see [Workspaces](/docs/07-workspaces/). |

For broader diagnostics, see [Troubleshooting](/docs/25-troubleshooting/).
