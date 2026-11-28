---
title: LeapMux
layout: hextra-home
---

<div class="hx:mt-6 hx:mb-4 hero-headline-with-ticker">
{{< hextra/hero-headline >}}
  <span class="hero-headline-line hero-headline-line--top">A workspace for a fleet</span> <span class="hero-headline-line hero-headline-line--bottom">of coding agents</span>
{{< /hextra/hero-headline >}}
{{< agent-ticker >}}
</div>

<div class="hx:mb-6">
{{< hextra/hero-subtitle >}}
  Run Claude Code, Codex, and {{< agent-provider-count >}} coding agents side by side &mdash; each in its own git worktree and branch,&nbsp;<br class="hx:sm:block hx:hidden" />tiled or floating, on a local or remote machine, with end-to-end encrypted traffic.
{{< /hextra/hero-subtitle >}}
</div>

<div class="lm-hero-actions">
  <a class="lm-btn-primary" id="lm-home-download-cta" href="/download/">Download LeapMux</a>
  <a class="lm-btn-secondary" href="/docs/getting-started/quick-start/">5-Minute Quick Start</a>
  <a class="lm-btn-secondary" href="/docs/">Read the Docs</a>
</div>

<p class="lm-hero-meta">
  macOS, Linux, Windows &amp; browser &middot; Bring your own agent subscriptions &middot; <a href="/docs/reference/supported-agents/">Compare all {{< agent-provider-count >}} supported agents</a>
</p>

<script>
(() => {
  const btn = document.getElementById('lm-home-download-cta');
  if (!btn) return;
  const hint = (((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || '') + ' ' + (navigator.userAgent || '')).toLowerCase();
  if (hint.includes('mac')) btn.textContent = 'Download for macOS';
  else if (hint.includes('win')) btn.textContent = 'Download for Windows';
  else if (hint.includes('linux')) btn.textContent = 'Download for Linux';
})();
</script>

<div class="hx:mb-8">
{{< hero-showcase >}}
</div>

<div class="lm-proof-strip">
  <div class="lm-proof-item">
    <div class="lm-proof-value">{{< agent-provider-count >}} coding agents</div>
    <div class="lm-proof-label">Unified chat, diffs, queueing, and permissions backed by an E2E-verified capability matrix.</div>
  </div>
  <div class="lm-proof-item">
    <div class="lm-proof-value">Hybrid post-quantum E2EE</div>
    <div class="lm-proof-label">Your Hub relays ciphertext between clients and Workers and cannot read agent chats, terminal output, or files.</div>
  </div>
  <div class="lm-proof-item">
    <div class="lm-proof-value">Zero inbound ports</div>
    <div class="lm-proof-label">Remote Workers on Linux servers or GPU boxes dial out over WebSockets through firewalls and NAT.</div>
  </div>
  <div class="lm-proof-item">
    <div class="lm-proof-value">Cross-platform &amp; self-hosted</div>
    <div class="lm-proof-label">Native desktop app plus browser UI for macOS, Linux, and Windows under FSL-1.1-ALv2 (Apache 2.0 after 2 years).</div>
  </div>
</div>

<div class="hx:mb-10">
  <h2 class="hx:text-xl hx:font-semibold hx:mb-3">Or let your coding agent introduce you</h2>
  <p class="hx:text-gray-600 hx:dark:text-gray-400 hx:mb-4">Paste this prompt into Claude Code, Codex, Cursor, or any other coding agent to pick a deployment mode, install LeapMux, and launch your first parallel session.</p>
{{< copy-prompt label="Ask your coding agent to set up LeapMux" >}}
Read https://leapmux.dev/agent-guide.md and help me choose the right LeapMux setup mode for my machine, install it, and start my first parallel coding-agent session.
{{< /copy-prompt >}}
</div>

<div class="lm-showcase">
  <section class="lm-showcase-row">
    <div class="lm-showcase-copy">
      <div class="lm-showcase-eyebrow">Parallel Worktrees &amp; Layout</div>
      <h2 class="lm-showcase-title">Parallel agents, zero collisions</h2>
      <p class="lm-showcase-text">Open each agent in its own git worktree and branch from one dialog. Tile agents beside a syntax-highlighted diff viewer and live PTY terminals, steer active turns before they finish, and clean up worktrees safely when you close the last tab.</p>
      <ul class="lm-showcase-list">
        <li>Create isolated git worktrees in one click</li>
        <li>Queue prompts and steer or preempt mid-turn</li>
        <li>Inspect unified and split diffs side by side</li>
        <li>Guard dirty worktrees before closing tabs</li>
      </ul>
      <a class="lm-showcase-link" href="/docs/recipes/parallel-agents/">Read the parallel agents recipe &rarr;</a>
    </div>
    <div class="lm-showcase-media">
      {{< video-clip src="parallel-worktrees" poster="tabs-tiling-layout" alt="Animated walkthrough of switching between parallel coding agents in isolated git worktrees, toggling Split diff view, and opening the Quake terminal" caption="Switch between isolated worktrees, toggle split diffs, and pop down a Quake terminal without losing focus." width="1280" height="800" >}}
    </div>
  </section>

  <section class="lm-showcase-row lm-showcase-row--reverse">
    <div class="lm-showcase-copy">
      <div class="lm-showcase-eyebrow">Remote Workers &amp; E2EE</div>
      <h2 class="lm-showcase-title">Encrypted devboxes behind NAT</h2>
      <p class="lm-showcase-text">Start solo on your laptop with one click, or pair a Hub with remote Workers on cloud VMs, office desktops, or GPU servers. Workers dial out over HTTPS/WebSocket, so you open zero inbound ports on your devboxes.</p>
      <ul class="lm-showcase-list">
        <li>Hybrid post-quantum E2EE to every Worker</li>
        <li>Live layout sync across desktop and browser</li>
        <li>Daemon sessions survive browser closes</li>
        <li>Outbound WebSockets with zero open ports</li>
      </ul>
      <a class="lm-showcase-link" href="/docs/recipes/remote-devbox/">Connect a remote devbox &rarr;</a>
    </div>
    <div class="lm-showcase-media">
      {{< video-clip src="remote-workers-tunnels" poster="register-worker-dialog" alt="Animated walkthrough of inspecting the online gpu-devbox Worker menu and minting POSIX or PowerShell registration commands" caption="Inspect connected remote Workers and register new machines behind NAT with a single one-time key." width="1280" height="800" >}}
    </div>
  </section>

  <section class="lm-showcase-row">
    <div class="lm-showcase-copy">
      <div class="lm-showcase-eyebrow">CLI &amp; Agent Skills</div>
      <h2 class="lm-showcase-title">Script your fleet from the CLI</h2>
      <p class="lm-showcase-text">Every workspace action is scriptable through the <code>leapmux</code> CLI (short alias <code>lmx</code>) and six built-in Agent Skills. Spawn worktree agents, wait without polling, enforce JSON schemas on replies, and forward TCP ports over encrypted channels.</p>
      <ul class="lm-showcase-list">
        <li>Wait on agents with <code>--wait</code> and <code>--any</code></li>
        <li>Enforce JSON schemas on agent replies</li>
        <li>Resume event streams and retry idempotently</li>
        <li>Six built-in skills for parallel workflows</li>
      </ul>
      <a class="lm-showcase-link" href="/docs/using/cli-guide/">Explore the CLI guide &rarr;</a>
    </div>
    <div class="lm-showcase-media">
      {{< cli-cast src="cli-fleet-demo" title="zsh — payments-api" poster="npt:0:20" caption="Live leapmux CLI capture: open Claude Code and Codex on isolated git worktrees, list active agents, dispatch tasks in parallel, wait for the first completion, and read its reply." >}}
A1=$(leapmux agent open --provider claude --worktree feat/api --base main -q)
A2=$(leapmux agent open --provider codex  --worktree feat/cli --base main -q)
leapmux agent list
leapmux agent send "$A1" "Implement the /health endpoint with tests."
leapmux agent send "$A2" "Add the health subcommand and JSON output."
leapmux agent wait "$A1" "$A2" --any --json
leapmux agent messages "$A1" --json | \
  jq -r '.data.messages[] | select(.source=="agent") | .reply_text // empty'
      {{< /cli-cast >}}
    </div>
  </section>
</div>

{{< changelog-strip count="3" >}}

<section class="lm-home-faq" aria-labelledby="lm-home-faq-heading">
  <div class="lm-home-faq-header">
    <div class="lm-showcase-eyebrow">FAQ</div>
    <h2 id="lm-home-faq-heading" class="lm-home-faq-title">Common questions</h2>
  </div>
  <div class="lm-home-faq-list">
    <details class="lm-home-faq-item">
      <summary>Do I need separate API keys or a LeapMux subscription?</summary>
      <p>No. Bring your existing coding-agent CLI subscriptions or API keys across all {{< agent-provider-count >}} supported providers. LeapMux drives the agent CLIs you already authenticate on your laptop or remote Workers.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>Can the Hub read my code, chats, or terminal output?</summary>
      <p>No. Clients and Workers establish hybrid post-quantum end-to-end encrypted (E2EE) channels directly. The Hub relays ciphertext only and cannot read agent chats, terminal output, or files. See <a href="/docs/admin/security-and-auth/#what-the-hub-can-and-cannot-see">What the Hub can and cannot see</a> for the full breakdown.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>Do my remote devboxes need inbound firewall ports open?</summary>
      <p>No. Workers dial out to the Hub over HTTPS and WebSockets, so remote servers, cloud VMs, and GPU boxes stay behind NAT with zero inbound firewall ports open. See <a href="/docs/recipes/remote-devbox/">Connect a Remote Devbox Behind NAT</a> for the walkthrough.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>What is the difference between Solo Desktop, Solo Daemon, and Distributed mode?</summary>
      <p>Solo Desktop runs a local Hub and Worker inside the native app with one click, while Solo Daemon runs the same single-machine setup headlessly for browser and CLI access. Distributed mode pairs a central Hub with one or more remote Workers for multi-machine or team setups — see <a href="/docs/getting-started/concepts/">Concepts &amp; Topologies</a>.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>What is the current status of LeapMux?</summary>
      <p>LeapMux is pre-release software under active development. Expect breaking changes as we refine the CLI, APIs, and workflows for the best long-term design. We welcome your feedback and bug reports on <a href="https://github.com/leapmux/leapmux/issues">GitHub Issues</a>.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>What license does LeapMux use?</summary>
      <p>LeapMux is source-available under the Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2), which converts to Apache 2.0 two years after each release. See <a href="/docs/reference/legal/">Legal</a> for full terms.</p>
    </details>
    <details class="lm-home-faq-item">
      <summary>Who built LeapMux?</summary>
      <p>LeapMux is built by Trustin Lee, the founder of <a href="https://netty.io/">Netty</a>, <a href="https://armeria.dev/">Armeria</a>, and <a href="https://line.github.io/centraldogma/">Central Dogma</a>, who previously built core infrastructure and developer tools at LINE, Databricks, Twitter, Apple, Red Hat, and Naver. He works as a Member of Technical Staff at depthfirst and develops LeapMux in his free time.</p>
      <div class="lm-home-faq-social">
        <a class="lm-social-icon-link" href="https://github.com/trustin" aria-label="Trustin Lee on GitHub" title="GitHub" target="_blank" rel="noopener noreferrer">
          <svg viewBox="0 0 24 24" width="20" height="20" fill="currentColor" aria-hidden="true"><path d="M12 3C7.0275 3 3 7.12937 3 12.2276c0 4.0833 2.57625 7.5321 6.15374 8.7548.45.0807.61875-.1961.61875-.4383 0-.2192-.01124-.9459-.01124-1.7187-2.26125.4268-2.84625-.5652-3.02625-1.0842-.10125-.2653-.54001-1.0843-.9225-1.3034-.315-.1731-.765-.5998-.01126-.6114.70876-.0115 1.21501.669 1.38375.9459.81 1.3956 2.10376 1.0035 2.62126.7612.07875-.5998.31495-1.0035.57375-1.2342-2.0025-.2306-4.095-1.0265-4.095-4.5561 0-1.0035.34875-1.834.92249-2.47994-.08999-.23069-.40499-1.17652.09001-2.44532 0 0 .75375-.24223 2.47499.94583.72001-.20762 1.48501-.31143 2.25001-.31143.765 0 1.53.10381 2.25.31143 1.7212-1.19959 2.475-.94583 2.475-.94583.4949 1.2688.18 2.21463.09 2.44532.5738.64594.9225 1.46484.9225 2.47994 0 3.5411-2.1037 4.3255-4.1063 4.5561.3263.2884.6076.8421.6076 1.7072 0 1.2342-.0113 2.2261-.0113 2.5376 0 .2422.1688.5305.6188.4383 1.7866-.6184 3.3391-1.7958 4.439-3.3662C20.4077 16.0457 20.9995 14.1611 21 12.2276 21 7.12937 16.9725 3 12 3z"/></svg>
        </a>
        <a class="lm-social-icon-link" href="https://linkedin.com/in/trustin" aria-label="Trustin Lee on LinkedIn" title="LinkedIn" target="_blank" rel="noopener noreferrer">
          <svg viewBox="0 0 24 24" width="20" height="20" fill="currentColor" aria-hidden="true"><path d="M19 3a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h14m-.5 15.5v-5.3a3.26 3.26 0 0 0-3.26-3.26c-.85 0-1.84.52-2.28 1.3v-1.11h-2.79v8.37h2.79v-4.93c0-.77.62-1.4 1.39-1.4a1.4 1.4 0 0 1 1.4 1.4v4.93h2.75M6.88 8.56a1.68 1.68 0 0 0 1.68-1.68c0-.93-.75-1.69-1.68-1.69a1.69 1.69 0 0 0-1.69 1.69c0 .93.76 1.68 1.69 1.68m1.39 9.94v-8.37H5.5v8.37h2.77z"/></svg>
        </a>
      </div>
    </details>
  </div>
</section>

<section class="lm-closing-cta">
  <h2>Ready to run your fleet?</h2>
  <p>Download LeapMux Desktop or start solo mode from the command line in under five minutes.</p>
  <div class="lm-download-hero-actions">
    <a class="lm-btn-primary" href="/download/">Download LeapMux</a>
    <a class="lm-btn-secondary" href="/docs/getting-started/quick-start/">5-Minute Quick Start</a>
    <a class="lm-btn-secondary" href="https://github.com/leapmux/leapmux/releases">Latest Releases on GitHub</a>
  </div>
</section>
