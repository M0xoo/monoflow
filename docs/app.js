/**
 * MonoFlow Interactive Systems Workbench & Playground (2026)
 */

document.addEventListener('DOMContentLoaded', () => {
  initClipboard();
  initWorkbench();
  initCalculator();
  initTheme();
  initSearch();
  initVideoPlayer();
});

// ============================================================================
// Clipboard Handler
// ============================================================================
function initClipboard() {
  document.querySelectorAll('.btn-copy').forEach(btn => {
    btn.addEventListener('click', async (e) => {
      e.preventDefault();
      const targetId = btn.getAttribute('data-target');
      let textToCopy = '';
      
      if (targetId) {
        const el = document.getElementById(targetId);
        if (el) textToCopy = el.innerText.trim();
      } else {
        const codeBlock = btn.closest('.code-container, .command-strip, .install-bar')?.querySelector('code, .install-cmd');
        if (codeBlock) textToCopy = codeBlock.innerText.trim();
      }

      if (!textToCopy) return;

      try {
        await navigator.clipboard.writeText(textToCopy);
        const originalText = btn.innerText;
        btn.innerText = 'COPIED ✓';
        btn.classList.add('copied');
        setTimeout(() => {
          btn.innerText = originalText;
          btn.classList.remove('copied');
        }, 1800);
      } catch (err) {
        console.error('Copy failed:', err);
      }
    });
  });
}

// ============================================================================
// Interactive Workbench
// ============================================================================
const workbenchContent = {
  cli: `
<div class="cli-preset-bar">
  <span class="cli-label">Select Command:</span>
  <button class="cli-btn" onclick="runCliCmd('synthesize')">$ monoflow synthesize</button>
  <button class="cli-btn" onclick="runCliCmd('checkpoint')">$ monoflow checkpoint</button>
  <button class="cli-btn" onclick="runCliCmd('worktree')">$ monoflow worktree spawn</button>
  <button class="cli-btn" onclick="runCliCmd('verify')">$ monoflow verify</button>
  <button class="cli-btn" onclick="runCliCmd('cache')">$ monoflow cache status</button>
  <button class="cli-btn" onclick="runCliCmd('version')">$ monoflow version bump</button>
  <button class="cli-btn" onclick="runCliCmd('subset')">$ monoflow subset check</button>
  <button class="cli-btn" onclick="runCliCmd('sync')">$ monoflow sync</button>
  <button class="cli-btn" onclick="runCliCmd('events')">$ monoflow events unread</button>
  <button class="cli-btn" onclick="runCliCmd('scenario')">$ monoflow scenario run</button>
  <button class="cli-btn" onclick="runCliCmd('receipt')">$ monoflow receipt sign</button>
</div>
<div id="terminal-stream">
<span class="c-amber c-bold">$ monoflow synthesize pkg/billing/service.go</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">PARSER</span> Loading Go AST trees for base (main), agent-1 (feat/ratelimit), agent-2 (feat/metrics)
<span class="c-muted">[00:00:00.008]</span> <span class="c-cyan">AST-DIFF</span> agent-1 added 1 import ("golang.org/x/time/rate"), 1 struct field (*rate.Limiter), 1 method (CheckQuota)
<span class="c-muted">[00:00:00.014]</span> <span class="c-cyan">AST-DIFF</span> agent-2 added 1 import ("github.com/prometheus/client_golang/prometheus"), 1 package var (chargeCounter)
<span class="c-muted">[00:00:00.019]</span> <span class="c-emerald">SYNTHESIZER</span> AST graph nodes disjoint; merging AST declarations without textual conflict
<span class="c-muted">[00:00:00.024]</span> <span class="c-emerald">SYNTHESIZER</span> Validating Go type checking and compilation signatures... OK
<span class="c-muted">[00:00:00.025]</span> <span class="c-amber">PLUMBING</span> Ephemeral tree hash written: <span class="c-purple">9fa84b1e7c902d33458fa...</span>
<span class="c-muted">[00:00:00.025]</span> <span class="c-emerald c-bold">SUCCESS</span> Synthesis complete in 24.8ms. Zero merge conflicts.
</div>`,

  ast: `
<span class="c-cyan">// === AST 3-Way Semantic Synthesis Inspector ===</span>
<span class="c-muted">// Target File:</span> pkg/billing/service.go
<span class="c-muted">// Status:</span> <span class="c-emerald">Zero git conflict. Synthesized automatically via Go AST grafting in 24.2ms.</span>

<span class="c-purple">package</span> billing

<span class="c-purple">import</span> (
  <span class="c-emerald">"github.com/prometheus/client_golang/prometheus"</span>  <span class="c-muted">// Grafted from Agent-2 (Metrics)</span>
  <span class="c-cyan">"golang.org/x/time/rate"</span>                           <span class="c-muted">// Grafted from Agent-1 (RateLimiter)</span>
  <span class="c-muted">"sync"</span>
)

<span class="c-purple">type</span> BillingService <span class="c-purple">struct</span> {
  mu      sync.RWMutex
  <span class="c-cyan">limiter *rate.Limiter</span>                               <span class="c-muted">// Added by Agent-1 (AST Node #428)</span>
}

<span class="c-muted">// Added by Agent-2 (AST Node #439)</span>
<span class="c-purple">var</span> <span class="c-emerald">chargeCounter</span> = prometheus.NewCounter(prometheus.CounterOpts{
  Name: "billing_charges_total",
})

<span class="c-purple">func</span> (s *BillingService) ProcessCharge(ctx context.Context, amount int64) error {
  <span class="c-cyan">if !s.limiter.Allow() {</span>                            <span class="c-muted">// Injected AST block (Agent-1)</span>
    <span class="c-rose">return ErrRateLimited</span>
  }
  <span class="c-emerald">chargeCounter.Inc()</span>                                <span class="c-muted">// Injected AST block (Agent-2)</span>
  <span class="c-muted">return s.executeTransaction(ctx, amount)</span>
}`,

  cow: `
<span class="c-cyan">// === APFS / btrfs Copy-on-Write Worktree Inspector ===</span>
<span class="c-muted">Command:</span> monoflow worktree spawn --agent worker-3 --share-caches

<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">VFS-DETECT</span> Host filesystem: Darwin APFS (supports clonefile() zero-copy CoW)
<span class="c-muted">[00:00:00.018]</span> <span class="c-cyan">CLONEFILE</span> Cloning repository tree to .agents/worktrees/worker-3
<span class="c-muted">[00:00:00.035]</span> <span class="c-amber">COW-CACHE</span> Cloning node_modules (420MB) -> Disk allocated: <span class="c-emerald">0 bytes</span> (shared physical blocks)
<span class="c-muted">[00:00:00.048]</span> <span class="c-amber">COW-CACHE</span> Cloning .venv (180MB)        -> Disk allocated: <span class="c-emerald">0 bytes</span> (shared physical blocks)
<span class="c-muted">[00:00:00.054]</span> <span class="c-cyan">GIT-ATTACH</span> Attaching worktree HEAD to ephemeral shadow branch agent/worker-3
<span class="c-muted">[00:00:00.058]</span> <span class="c-emerald c-bold">READY</span> Virtual worktree worker-3 provisioned in <span class="c-emerald">58.4ms</span>.
<span class="c-muted">Active Worktrees:</span>
  • worker-1: branch=feat/auth-jwt  lease=5m42s   dirty_files=2   cow_blocks=14MB
  • worker-2: branch=feat/db-pool   lease=8m10s   dirty_files=4   cow_blocks=28MB
  • worker-3: branch=feat/billing   lease=10m00s  dirty_files=0   cow_blocks=0MB`,

  audit: `
<span class="c-rose">// === Agentic Slop & Scope Auditor Diagnostic ===</span>
<span class="c-muted">PR:</span> #142 (Author: autonomous-agent-claude-3.7)
<span class="c-muted">Intent Target:</span> "Add exponential backoff retry to payment webhook client"

<span class="c-cyan">AUDIT RESULTS:</span>
  [✓] <span class="c-emerald">INTENT RELEVANCE SCORE: 94.2%</span> (pkg/payment/webhook.go matches declared intent)
  [✓] <span class="c-emerald">TEST INTEGRITY CHECK: PASS</span> (0 test assertions altered or deleted)
  [!] <span class="c-rose c-bold">HALLUCINATION DETECTED:</span>
      File: pkg/payment/types.go (Line 184)
      Diff: Replaced 14 lines with unprompted refactoring of unrelated CustomerID schema.
      <span class="c-rose">Action: Auto-rejected diff chunk #3; quarantined to sandbox.</span>
  [✓] <span class="c-emerald">PROMPT INJECTION SCAN: CLEAN</span> (No system prompt bypass attempts in docstrings)

<span class="c-muted">Gate Status:</span> <span class="c-amber">AUTO-TRIMMED</span> (Discarded hallucinated chunk, accepted focused webhook patch).`,

  queue: `
<span class="c-cyan">// === Speculative DAG Merge Queue ===</span>
<span class="c-muted">Base Commit:</span> origin/main (SHA: 41b8a902)
<span class="c-muted">Active Parallel PRs in Speculative Flight:</span> 4

<span class="c-muted">TOPOLOGY DAG:</span>
  ├── <span class="c-emerald">PR #204 (pkg/auth)</span>      ──> Affected: [pkg/auth]            ──> Status: <span class="c-emerald">SPECULATIVE PASS (Merged ahead of CI)</span>
  ├── <span class="c-emerald">PR #205 (pkg/analytics)</span> ──> Affected: [pkg/analytics]       ──> Status: <span class="c-emerald">SPECULATIVE PASS (Disjoint DAG node)</span>
  ├── <span class="c-amber">PR #206 (pkg/api)</span>       ──> Affected: [pkg/api, pkg/auth]   ──> Status: <span class="c-amber">AUTO-REBASING ON PR #204 (< 40ms)</span>
  └── <span class="c-cyan">PR #207 (pkg/database)</span>  ──> Affected: [pkg/database]        ──> Status: <span class="c-cyan">AWAITING 1S POW RECEIPT</span>

<span class="c-muted">Merge Queue Throughput:</span> <span class="c-emerald">4 PRs processed in 4.2 seconds</span> (Traditional CI would take 52 minutes).`,

  pow: `
<span class="c-cyan">// === 1-Second Reviewer Proof-of-Work Receipt (.monoflow/proofs/pr-204.json) ===</span>
{
  <span class="c-amber">"version"</span>: "1.0",
  <span class="c-amber">"pr_number"</span>: 204,
  <span class="c-amber">"base_tree_sha"</span>: <span class="c-purple">"41b8a902d847c1b50ef91a7834bc2174"</span>,
  <span class="c-amber">"head_tree_sha"</span>: <span class="c-purple">"9fa84b1e7c902d33458fa6c2b1897e01"</span>,
  <span class="c-amber">"affected_packages"</span>: [<span class="c-emerald">"pkg/auth"</span>, <span class="c-emerald">"pkg/auth/jwt"</span>],
  <span class="c-amber">"execution_telemetry"</span>: {
    <span class="c-cyan">"ast_synthesis_ms"</span>: 24.8,
    <span class="c-cyan">"test_execution_ms"</span>: 1140.2,
    <span class="c-cyan">"tests_passed"</span>: 48,
    <span class="c-cyan">"tests_quarantined"</span>: 0
  },
  <span class="c-amber">"reviewer_verification"</span>: {
    <span class="c-cyan">"verifier"</span>: "monoflow-pow-engine",
    <span class="c-cyan">"sha256_tree_signature"</span>: <span class="c-purple">"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"</span>,
    <span class="c-cyan">"verification_cost"</span>: <span class="c-emerald">"0.159ms"</span>,
    <span class="c-cyan">"gate_verdict"</span>: <span class="c-emerald c-bold">"PROVEN_PASS"</span>
  }
}`,

  cache: `
<span class="c-cyan">// === Deterministic Content-Addressable Package Cache & Daemon ===</span>
<span class="c-muted">Storage Engine:</span> .agents/cache (Local LevelDB + Artifact Tarballs)
<span class="c-muted">Daemon Service:</span> monoflow cache serve --port=8080 (Zero-dependency HTTP Daemon)

<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">KEYGEN</span> Computing deterministic cache key for target "pkg/billing"...
<span class="c-muted">[00:00:00.003]</span> <span class="c-purple">HASH</span> SHA-256 Digest: <span class="c-amber">5a580c3951e7a7cf560dede4d7c1a875...</span> (Source + Toolchain + Config)
<span class="c-muted">[00:00:00.004]</span> <span class="c-cyan">CACHE-QUERY</span> Inspecting local store (.agents/cache/verify/)...
<span class="c-muted">[00:00:00.005]</span> <span class="c-emerald c-bold">CACHE HIT (0.8ms)</span> Verified via cryptographic match! Zero test re-execution required.
<span class="c-muted">[00:00:00.008]</span> <span class="c-cyan">HTTP-DAEMON</span> Active endpoints:
  • GET  /v1/cache/{key}     <span class="c-emerald">200 OK (0.24ms)</span>
  • PUT  /v1/cache/{key}     <span class="c-emerald">201 Created (1.12ms)</span>
  • GET  /v1/metrics         <span class="c-purple">hit_ratio=94.2% entries=18 total_size=14.2MB</span>
  • GET  /health             <span class="c-emerald">{"status": "healthy"}</span>
<span class="c-muted">Cache Health:</span> <span class="c-emerald">Hit Ratio: 94.2%</span> | <span class="c-amber">Local Entries: 18</span> | <span class="c-cyan">Storage: 14.2 MB</span>`,

  version: `
<span class="c-cyan">// === SemVer Versioning, Changeset Cascade & Subset Health ===</span>
<span class="c-muted">Command:</span> monoflow version bump billing minor --cascade --reason="Add stripe webhook support"

<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">VERSION</span> Bumping package "billing": 1.4.0 ──> <span class="c-emerald">1.5.0 (minor)</span>
<span class="c-muted">[00:00:00.005]</span> <span class="c-cyan">CHANGESET</span> Recorded changeset: <span class="c-purple">.agents/changesets/cs-billing-17916480.json</span>
<span class="c-muted">[00:00:00.008]</span> <span class="c-amber">DAG-CASCADE</span> Resolving downstream dependents across topological graph:
  └── <span class="c-purple">pkg/gateway</span> depends on billing (>= 1.4.0) ──> Cascading bump: 2.1.3 ──> <span class="c-emerald">2.1.4 (patch)</span>
<span class="c-muted">[00:00:00.012]</span> <span class="c-emerald c-bold">CASCADE RESOLVED:</span> 2 packages queued for manifest update in 4.8ms.
<span class="c-muted">[00:00:00.015]</span> <span class="c-cyan">SUBSET-HEALTH</span> Verifying active sparse subset cone integrity...
  [✓] All 2 upstream dependencies present in cone. 0 missing packages.
<span class="c-muted">Status:</span> <span class="c-emerald">Ready for atomic manifest application (monoflow version apply)</span>`,

  events: `
<span class="c-cyan">// === Asynchronous Reactive Event Inbox & CI Watcher ===</span>
<span class="c-muted">Repository:</span> origin/main (Commit: 82009be2)
<span class="c-muted">Supervision Mode:</span> Autonomous background watcher (Zero context tokens burned)

<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">EVENT-QUERY</span> Reading .agents/events/ directory in <span class="c-emerald">3.4ms</span>:
<span class="c-muted">Active Events:</span>
  • <span class="c-purple">evt_ci_passed_82009be2</span>: type=ci_run status=<span class="c-emerald">SUCCESS</span> duration=73s (Workflow: monoflow-ci.yml)
  • <span class="c-purple">evt_pr_reviewed_pr-2</span>:   type=pr_review verdict=<span class="c-emerald">APPROVED</span> tier=TIER_1 (Auto-Approvable)
  • <span class="c-purple">evt_pr_merged_pr-2</span>:     type=pr_merge status=<span class="c-emerald">MERGED</span> strategy=squash (Speculative Queue Pass)
<span class="c-muted">[00:00:00.004]</span> <span class="c-cyan">STATUS</span> 0 unread alerts requiring agent intervention.
<span class="c-muted">Reactive Directive:</span> <span class="c-emerald c-bold">Agent remains unblocked. Zero polling loops required.</span>`,

  scenario: `
<span class="c-cyan">// === Enterprise Multi-Agent Monorepo Scenario Simulator ===</span>
<span class="c-muted">Command:</span> monoflow scenario run --json

<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">SCENARIO</span> Provisioning ephemeral enterprise monorepo workspace (Go, TS, Python)...
<span class="c-muted">[00:00:00.045]</span> <span class="c-cyan">AGENT-1</span> Provisioned CoW worktree "agent-billing" (<span class="c-emerald">52ms</span>). Target: pkg/billing.
<span class="c-muted">[00:00:00.098]</span> <span class="c-cyan">AGENT-2</span> Provisioned CoW worktree "agent-analytics" (<span class="c-emerald">48ms</span>). Target: pkg/analytics.
<span class="c-muted">[00:00:00.320]</span> <span class="c-amber">AST-SYNTH</span> Concurrent edits detected on shared models.go ──> <span class="c-emerald">AST Synthesized in 18ms</span>.
<span class="c-muted">[00:00:00.560]</span> <span class="c-cyan">RISK-AUDIT</span> Machine risk evaluated: Tier 1 (Auto-Approvable, blast radius: 2 pkgs).
<span class="c-muted">[00:00:00.820]</span> <span class="c-cyan">SPEC-QUEUE</span> Speculative local rebase onto main simulated in <span class="c-emerald">21ms</span>.
<span class="c-muted">[00:00:01.120]</span> <span class="c-emerald">CI-GATE</span> Two-tier CI gate validated signed PoW receipt. Tier 1 Fast-Path passed.
<span class="c-muted">[00:00:01.340]</span> <span class="c-purple">DELIVER</span> Hermetic production deliverable built at .agents/dist/full-app.
<span class="c-muted">[00:00:01.345]</span> <span class="c-emerald c-bold">SCENARIO PASSED:</span> 10-step multi-agent enterprise workflow verified in 1.34s!
`
};

function initWorkbench() {
  const tabs = document.querySelectorAll('.wb-tab');
  const viewport = document.getElementById('workbench-viewport');
  if (!viewport) return;

  // Set initial content
  viewport.innerHTML = workbenchContent.cli;

  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      tabs.forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      const target = tab.getAttribute('data-target');
      if (workbenchContent[target]) {
        viewport.innerHTML = workbenchContent[target];
      }
    });
  });
}

// Global CLI command simulator for workbench
window.runCliCmd = function(cmd) {
  const stream = document.getElementById('terminal-stream');
  if (!stream) return;

  const commands = {
    synthesize: `
<span class="c-amber c-bold">$ monoflow synthesize pkg/billing/service.go</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">PARSER</span> Loading Go AST trees for base (main), agent-1 (feat/ratelimit), agent-2 (feat/metrics)
<span class="c-muted">[00:00:00.008]</span> <span class="c-cyan">AST-DIFF</span> agent-1 added 1 import, 1 struct field, 1 method
<span class="c-muted">[00:00:00.014]</span> <span class="c-cyan">AST-DIFF</span> agent-2 added 1 import, 1 package var
<span class="c-muted">[00:00:00.019]</span> <span class="c-emerald">SYNTHESIZER</span> AST graph nodes disjoint; merging AST declarations without textual conflict
<span class="c-muted">[00:00:00.024]</span> <span class="c-emerald">SYNTHESIZER</span> Validating Go type checking signatures... OK
<span class="c-muted">[00:00:00.025]</span> <span class="c-emerald c-bold">SUCCESS</span> Synthesis complete in 24.8ms. Zero merge conflicts.`,

    checkpoint: `
<span class="c-amber c-bold">$ monoflow checkpoint</span>
<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">PLUMBING</span> Invoking git write-tree on shadow index...
<span class="c-muted">[00:00:00.011]</span> <span class="c-cyan">TREE-SHA</span> Created tree object: 7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1f
<span class="c-muted">[00:00:00.014]</span> <span class="c-emerald c-bold">CHECKPOINT SAVED</span> in 14.2ms. Workdir clean; zero index pollution.`,

    worktree: `
<span class="c-amber c-bold">$ monoflow worktree spawn --agent worker-4 --share-caches</span>
<span class="c-muted">[00:00:00.003]</span> <span class="c-cyan">VFS-DETECT</span> Apple APFS clonefile() supported
<span class="c-muted">[00:00:00.021]</span> <span class="c-cyan">CLONEFILE</span> CoW clone of node_modules and .venv completed (0 bytes copied)
<span class="c-muted">[00:00:00.058]</span> <span class="c-emerald c-bold">READY</span> Virtual worktree worker-4 spawned in 58.1ms at .agents/worktrees/worker-4`,

    verify: `
<span class="c-amber c-bold">$ monoflow verify --scope affected</span>
<span class="c-muted">[00:00:00.005]</span> <span class="c-cyan">DAG-GRAPH</span> Computed affected boundary: [pkg/auth, pkg/gateway]
<span class="c-muted">[00:00:00.012]</span> <span class="c-cyan">TEST-RUNNER</span> Executing tests in parallel for affected packages...
<span class="c-muted">[00:01:00.180]</span> <span class="c-emerald">PASS</span> pkg/auth (24 tests passed in 680ms)
<span class="c-muted">[00:01:00.210]</span> <span class="c-emerald">PASS</span> pkg/gateway (18 tests passed in 490ms)
<span class="c-muted">[00:01:00.215]</span> <span class="c-emerald c-bold">ALL CHECKS PASSED</span> in 1.18s total wall time.`,

    receipt: `
<span class="c-amber c-bold">$ monoflow receipt sign --pr 208</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">CRYPTO</span> Signing affected package test digests with repository key...
<span class="c-muted">[00:00:00.003]</span> <span class="c-emerald">RECEIPT GENERATED</span> Written to .monoflow/proofs/pr-208.json
<span class="c-muted">[00:00:00.004]</span> <span class="c-emerald c-bold">VERIFICATION PROOF:</span> SHA-256 e3b0c44298fc1c14... Reviewers verify in 0.159ms.`,

    cache: `
<span class="c-amber c-bold">$ monoflow cache status</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">CACHE</span> Cache directory: .agents/cache
<span class="c-muted">[00:00:00.003]</span> <span class="c-cyan">METRICS</span> Verification entries: 18 | Artifact tarballs: 8
<span class="c-muted">[00:00:00.005]</span> <span class="c-emerald">HIT-RATIO</span> 94.2% (148 hits, 9 misses)
<span class="c-muted">[00:00:00.006]</span> <span class="c-purple">STORAGE</span> Total size on disk: 14.2 MB
<span class="c-muted">[00:00:00.008]</span> <span class="c-emerald c-bold">STATUS: OK</span> Local and remote cache synchronized.`,

    version: `
<span class="c-amber c-bold">$ monoflow version bump billing minor --cascade</span>
<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">VERSION</span> Bumping "billing" from 1.4.0 to <span class="c-emerald">1.5.0</span>
<span class="c-muted">[00:00:00.004]</span> <span class="c-purple">CHANGESET</span> Saved changeset to .agents/changesets/cs-billing.json
<span class="c-muted">[00:00:00.007]</span> <span class="c-cyan">CASCADE</span> Found downstream dependent: "gateway"
<span class="c-muted">[00:00:00.009]</span> <span class="c-amber">CASCADED</span> Bumping "gateway" from 2.1.3 to <span class="c-emerald">2.1.4</span>
<span class="c-muted">[00:00:00.012]</span> <span class="c-emerald c-bold">SUCCESS</span> 2 packages bumped and recorded in 4.8ms.`,

    subset: `
<span class="c-amber c-bold">$ monoflow subset check</span>
<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">SUBSET</span> Active cone packages: [billing, gateway]
<span class="c-muted">[00:00:00.005]</span> <span class="c-cyan">HEALTH</span> Inspecting transitive dependencies in DAG...
<span class="c-muted">[00:00:00.007]</span> <span class="c-emerald">PASS</span> billing: all dependencies present (contracts, auth)
<span class="c-muted">[00:00:00.008]</span> <span class="c-emerald">PASS</span> gateway: all dependencies present (billing, contracts)
<span class="c-muted">[00:00:00.009]</span> <span class="c-emerald c-bold">HEALTHY</span> Active sparse subset cone is 100% complete and compilable.`,

    sync: `
<span class="c-amber c-bold">$ monoflow sync origin main</span>
<span class="c-muted">[00:00:00.015]</span> <span class="c-cyan">FETCH</span> Fetching latest refs from origin/main (sparse cone preserved)...
<span class="c-muted">[00:00:00.042]</span> <span class="c-cyan">FAST-FORWARD</span> Fast-forwarding local branch to 82009be2...
<span class="c-muted">[00:00:00.058]</span> <span class="c-emerald c-bold">SYNC COMPLETE</span> Synchronized with remote in 58.2ms without raw Git commands.`,

    events: `
<span class="c-amber c-bold">$ monoflow events unread</span>
<span class="c-muted">[00:00:00.001]</span> <span class="c-cyan">INBOX</span> Querying .agents/events/ in 3.4ms...
<span class="c-muted">[00:00:00.003]</span> <span class="c-emerald">EVENT</span> evt_ci_passed_82009be2: GitHub Actions Run #3805374 <span class="c-emerald">PASSED</span> (73s)
<span class="c-muted">[00:00:00.004]</span> <span class="c-cyan">EVENT</span> evt_pr_reviewed_pr-2: Reviewer approved PR #2 (TIER_1)
<span class="c-muted">[00:00:00.005]</span> <span class="c-emerald c-bold">DIRECTIVE:</span> All remote tasks succeeded. 0 blocking errors.`,

    scenario: `
<span class="c-amber c-bold">$ monoflow scenario run --json</span>
<span class="c-muted">[00:00:00.004]</span> <span class="c-cyan">START</span> Initializing multi-agent enterprise monorepo benchmark...
<span class="c-muted">[00:00:00.052]</span> <span class="c-emerald">AGENT-1</span> Provisioned CoW worktree agent-1 (52ms)
<span class="c-muted">[00:00:00.098]</span> <span class="c-emerald">AGENT-2</span> Provisioned CoW worktree agent-2 (48ms)
<span class="c-muted">[00:00:00.340]</span> <span class="c-emerald">AST-MERGE</span> Synthesized concurrent modifications on shared models in 18ms
<span class="c-muted">[00:00:00.580]</span> <span class="c-emerald">RISK</span> Machine evaluated risk: Tier 1 (Auto-Approvable)
<span class="c-muted">[00:00:00.810]</span> <span class="c-emerald">QUEUE</span> Speculative pre-flight rebase passed in 21ms
<span class="c-muted">[00:00:01.345]</span> <span class="c-emerald c-bold">SCENARIO COMPLETE:</span> Full swarm pipeline executed in 1.34s with zero conflicts!`
  };

  if (commands[cmd]) {
    stream.innerHTML = commands[cmd];
  }
};

// ============================================================================
// ROI Calculator
// ============================================================================
function initCalculator() {
  const agentsSlider = document.getElementById('calc-agents');
  const prsSlider = document.getElementById('calc-prs');
  const hoursSavedEl = document.getElementById('calc-hours-saved');
  const costSavedEl = document.getElementById('calc-cost-saved');
  const agentsValEl = document.getElementById('calc-agents-val');
  const prsValEl = document.getElementById('calc-prs-val');

  if (!agentsSlider || !prsSlider) return;

  function update() {
    const agents = parseInt(agentsSlider.value, 10);
    const prs = parseInt(prsSlider.value, 10);

    if (agentsValEl) agentsValEl.innerText = agents;
    if (prsValEl) prsValEl.innerText = prs;

    // Traditional CI: 25 mins per PR.
    // MonoFlow CI: 1.5 mins per PR.
    // Time saved per PR: 23.5 mins = 0.391 hours.
    // Monthly PRs: prs * 22 working days.
    const monthlyPRs = prs * 22;
    const hoursSaved = Math.round(monthlyPRs * 0.391);
    
    // Cloud Runner cost ($0.03/min) + Agent token/compute stall costs ($15/hr)
    const costSaved = Math.round(hoursSaved * 42);

    if (hoursSavedEl) hoursSavedEl.innerText = hoursSaved.toLocaleString() + ' hrs';
    if (costSavedEl) costSavedEl.innerText = '$' + costSaved.toLocaleString();
  }

  agentsSlider.addEventListener('input', update);
  prsSlider.addEventListener('input', update);
  update();
}

// ============================================================================
// Theme Switcher
// ============================================================================
function initTheme() {
  const toggle = document.getElementById('theme-toggle');
  if (!toggle) return;

  const currentTheme = localStorage.getItem('monoflow_theme') || 'dark';
  document.documentElement.setAttribute('data-theme', currentTheme);
  toggle.innerText = currentTheme === 'dark' ? '☀' : '☾';

  toggle.addEventListener('click', () => {
    const theme = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', theme);
    localStorage.setItem('monoflow_theme', theme);
    toggle.innerText = theme === 'dark' ? '☀' : '☾';
  });
}

// ============================================================================
// Command Palette Search (⌘K)
// ============================================================================
function initSearch() {
  window.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault();
      const searchTrigger = document.querySelector('.search-trigger');
      if (searchTrigger) searchTrigger.click();
    }
  });

  const searchTrigger = document.querySelector('.search-trigger');
  if (searchTrigger) {
    searchTrigger.addEventListener('click', () => {
      const query = prompt('Quick jump: Type a topic (e.g., cache, version, ast, cow, scenario, events, cli, pow, benchmarks):');
      if (!query) return;
      const q = query.toLowerCase();
      if (q.includes('cache')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const tab = document.querySelector('[data-target="cache"]');
        if (tab) tab.click();
      } else if (q.includes('version') || q.includes('changeset') || q.includes('bump')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const tab = document.querySelector('[data-target="version"]');
        if (tab) tab.click();
      } else if (q.includes('event') || q.includes('push') || q.includes('inbox') || q.includes('watcher')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const tab = document.querySelector('[data-target="events"]');
        if (tab) tab.click();
      } else if (q.includes('scenario') || q.includes('swarm') || q.includes('sim')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const tab = document.querySelector('[data-target="scenario"]');
        if (tab) tab.click();
      } else if (q.includes('ast') || q.includes('synth')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const astTab = document.querySelector('[data-target="ast"]');
        if (astTab) astTab.click();
      } else if (q.includes('cow') || q.includes('worktree')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const cowTab = document.querySelector('[data-target="cow"]');
        if (cowTab) cowTab.click();
      } else if (q.includes('queue') || q.includes('spec')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const queueTab = document.querySelector('[data-target="queue"]');
        if (queueTab) queueTab.click();
      } else if (q.includes('audit') || q.includes('slop')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const auditTab = document.querySelector('[data-target="audit"]');
        if (auditTab) auditTab.click();
      } else if (q.includes('bench') || q.includes('perf')) {
        document.getElementById('benchmarks')?.scrollIntoView({ behavior: 'smooth' });
      } else if (q.includes('cli') || q.includes('command')) {
        document.getElementById('cli')?.scrollIntoView({ behavior: 'smooth' });
      } else if (q.includes('pow') || q.includes('receipt') || q.includes('review')) {
        document.getElementById('workbench')?.scrollIntoView({ behavior: 'smooth' });
        const powTab = document.querySelector('[data-target="pow"]');
        if (powTab) powTab.click();
      } else {
        document.getElementById('architecture')?.scrollIntoView({ behavior: 'smooth' });
      }
    });
  }
}

// ============================================================================
// Programmatic 90-Second Systems Video Simulator (1080p 60fps)
// ============================================================================
const videoChapters = [
  {
    id: 1,
    title: "01 / Monorepo Discovery & DAG Graph Compilation",
    startTime: 0,
    endTime: 15,
    agentState: [
      { name: "Orchestrator", task: "monoflow init --discover", status: "Active (Mapping 14 pkgs)", cls: "active-orch" },
      { name: "Worker-1", task: "Awaiting task dispatch", status: "Idle", cls: "" },
      { name: "Worker-2", task: "Awaiting task dispatch", status: "Idle", cls: "" }
    ],
    hudStats: {
      dagNodes: "14 nodes (28 edges)",
      discoveryTime: "18.4 ms",
      activeWorktrees: "0 / 16",
      conflictRisk: "0.0%"
    },
    terminal: `
<span class="c-amber c-bold">$ monoflow init . --discover</span>
<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">DISCOVERY</span> Scanning workspace roots (Go, TypeScript, Python, Proto)...
<span class="c-muted">[00:00:00.008]</span> <span class="c-cyan">DISCOVERY</span> Detected Go root: github.com/antigravity/monoflow
<span class="c-muted">[00:00:00.012]</span> <span class="c-cyan">DAG-BUILD</span> Constructing in-memory dependency graph:
  ├── <span class="c-purple">cmd/monoflow</span> ──> [pkg/checkpoint, pkg/verify, pkg/queue, pkg/events]
  ├── <span class="c-purple">pkg/verify</span>   ──> [pkg/graph, pkg/guard, pkg/checkpoint]
  └── <span class="c-purple">docs</span>         ──> [monoflow]
<span class="c-muted">[00:00:00.018]</span> <span class="c-emerald c-bold">DISCOVERY COMPLETE</span> in 18.4ms. Saved to .agents/monoflow.config.json.
<span class="c-muted">[00:00:00.020]</span> <span class="c-cyan">DAG-VERIFY</span> Acyclic validation passed. 0 circular dependency deadlocks detected.`
  },
  {
    id: 2,
    title: "02 / Parallel Swarm Provisioning: CoW Worktrees",
    startTime: 15,
    endTime: 30,
    agentState: [
      { name: "Worker-1 (Claude)", task: "feat: JWT auth rate limiter", status: "APFS CoW worktree", cls: "active-1" },
      { name: "Worker-2 (Cursor)", task: "feat: Prometheus metrics", status: "APFS CoW worktree", cls: "active-2" },
      { name: "Orchestrator", task: "CoW lease monitor (APFS)", status: "Active", cls: "active-orch" }
    ],
    hudStats: {
      dagNodes: "14 nodes",
      discoveryTime: "18.4 ms",
      activeWorktrees: "2 / 16 (0 KB disk added)",
      conflictRisk: "0.0%"
    },
    terminal: `
<span class="c-amber c-bold">$ monoflow worktree spawn --agent worker-1 --share-caches</span>
<span class="c-muted">[00:00:00.003]</span> <span class="c-cyan">VFS-DETECT</span> Apple APFS clonefile() primitive available.
<span class="c-muted">[00:00:00.018]</span> <span class="c-cyan">CLONEFILE</span> .agents/worktrees/worker-1 created with zero block replication.
<span class="c-muted">[00:00:00.035]</span> <span class="c-amber">COW-CACHE</span> node_modules (420MB) -> <span class="c-emerald">0 bytes allocated</span>
<span class="c-muted">[00:00:00.048]</span> <span class="c-amber">COW-CACHE</span> .venv (180MB)        -> <span class="c-emerald">0 bytes allocated</span>
<span class="c-muted">[00:00:00.058]</span> <span class="c-emerald c-bold">READY</span> worker-1 spawned in <span class="c-emerald">58.1ms</span>.
<span class="c-amber c-bold">$ monoflow worktree spawn --agent worker-2 --share-caches</span>
<span class="c-muted">[00:00:00.056]</span> <span class="c-emerald c-bold">READY</span> worker-2 spawned in <span class="c-emerald">56.4ms</span>.
<span class="c-muted">[00:00:00.060]</span> <span class="c-cyan">SWARM</span> 2 agents now working concurrently in total isolation.`
  },
  {
    id: 3,
    title: "03 / Zero-Lock Concurrency: Same-File AST Synthesis",
    startTime: 30,
    endTime: 45,
    agentState: [
      { name: "Worker-1", task: "Added rate.Limiter struct field", status: "AST Node #412", cls: "active-1" },
      { name: "Worker-2", task: "Added chargeCounter metric", status: "AST Node #429", cls: "active-2" },
      { name: "Synthesizer", task: "3-Way Go AST Semantic Graft", status: "24.8ms (0 conflicts)", cls: "active-orch" }
    ],
    hudStats: {
      dagNodes: "14 nodes",
      discoveryTime: "18.4 ms",
      activeWorktrees: "2 / 16",
      conflictRisk: "RESOLVED (< 25ms)"
    },
    terminal: `
<span class="c-rose c-bold">[EVENT] Concurrent edits detected on same file: pkg/billing/service.go</span>
<span class="c-amber c-bold">$ monoflow synthesize pkg/billing/service.go</span>
<span class="c-muted">[00:00:00.002]</span> <span class="c-cyan">PARSER</span> Loading Go AST trees: base (main), worker-1, worker-2
<span class="c-muted">[00:00:00.009]</span> <span class="c-cyan">AST-DIFF</span> worker-1: +1 import ("golang.org/x/time/rate"), +1 field (limiter)
<span class="c-muted">[00:00:00.015]</span> <span class="c-cyan">AST-DIFF</span> worker-2: +1 import ("prometheus"), +1 package var (chargeCounter)
<span class="c-muted">[00:00:00.019]</span> <span class="c-emerald">SYNTHESIZER</span> Declarations are disjoint; grafting AST nodes without text lock
<span class="c-muted">[00:00:00.024]</span> <span class="c-emerald">TYPECHECK</span> go vet & type checking signatures... <span class="c-emerald">OK</span>
<span class="c-muted">[00:00:00.025]</span> <span class="c-emerald c-bold">SUCCESS</span> Synthesis complete in <span class="c-emerald">24.8ms</span>! Zero git merge conflicts.`
  },
  {
    id: 4,
    title: "04 / Sub-2s Inner Loop & 14ms Shadow Snapshots",
    startTime: 45,
    endTime: 60,
    agentState: [
      { name: "Worker-1", task: "Running TIA verification", status: "PASS (680ms)", cls: "active-1" },
      { name: "Worker-2", task: "Running TIA verification", status: "PASS (460ms)", cls: "active-2" },
      { name: "Checkpoint Engine", task: "Shadow git write-tree", status: "14.2ms (chk_042)", cls: "active-orch" }
    ],
    hudStats: {
      dagNodes: "14 nodes",
      discoveryTime: "18.4 ms",
      activeWorktrees: "2 / 16",
      conflictRisk: "0.0%"
    },
    terminal: `
<span class="c-amber c-bold">$ monoflow verify --scope affected</span>
<span class="c-muted">[00:00:00.005]</span> <span class="c-cyan">TIA</span> Affected packages computed from in-memory DAG: [pkg/auth, pkg/billing]
<span class="c-muted">[00:00:00.008]</span> <span class="c-cyan">SKIPPED</span> 12 unaffected packages skipped from verification
<span class="c-muted">[00:00:00.680]</span> <span class="c-emerald">PASS</span> pkg/auth (24 tests passed in 680ms)
<span class="c-muted">[00:00:01.140]</span> <span class="c-emerald">PASS</span> pkg/billing (32 tests passed in 460ms)
<span class="c-muted">[00:00:01.142]</span> <span class="c-emerald c-bold">ALL CHECKS PASSED</span> in 1.14s total wall time!
<span class="c-amber c-bold">$ monoflow checkpoint save "feat: auth and billing verified"</span>
<span class="c-muted">[00:00:00.014]</span> <span class="c-emerald c-bold">CHECKPOINT SAVED</span> in 14.2ms. Tree hash: <span class="c-purple">9fa84b1e7c90...</span> (chk_042)`
  },
  {
    id: 5,
    title: "05 / Integrity Guardrails & PR Slop Auditor",
    startTime: 60,
    endTime: 75,
    agentState: [
      { name: "Worker-1", task: "Attempted test deletion", status: "BLOCKED (Anti-Cheat)", cls: "active-1" },
      { name: "PR Auditor", task: "Scanned AST diff chunk", status: "Hallucination Quarantined", cls: "active-orch" },
      { name: "Circuit Breaker", task: "Auto-reverted to chk_042", status: "Pristine State", cls: "active-orch" }
    ],
    hudStats: {
      dagNodes: "14 nodes",
      discoveryTime: "18.4 ms",
      activeWorktrees: "2 / 16",
      conflictRisk: "BLOCKED & REPAIRED"
    },
    terminal: `
<span class="c-rose c-bold">[SECURITY AUDIT] Agent attempted to delete 4 test assertions in auth_test.go</span>
<span class="c-amber c-bold">$ monoflow guard check</span>
<span class="c-muted">[00:00:00.012]</span> <span class="c-rose c-bold">VIOLATION DETECTED:</span> Anti-cheating guard flagged deleted test assertions!
<span class="c-muted">[00:00:00.016]</span> <span class="c-rose">AUTO-REVERT:</span> Working tree restored to chk_042 in 18ms.
<span class="c-amber c-bold">$ monoflow review audit --pr 204</span>
<span class="c-muted">[00:00:00.022]</span> <span class="c-cyan">INTENT-SCORE</span> 94.2% match against declared task contract.
<span class="c-muted">[00:00:00.026]</span> <span class="c-rose">HALLUCINATION:</span> 1 unprompted CustomerID schema refactor quarantined.
<span class="c-muted">[00:00:00.028]</span> <span class="c-emerald c-bold">GATE STATUS:</span> Auto-trimmed prompt slop; accepted focused webhook patch.`
  },
  {
    id: 6,
    title: "06 / Fire-and-Forget Push & Autonomous CI Supervisor",
    startTime: 75,
    endTime: 90,
    agentState: [
      { name: "Worker-1", task: "Moving to next backlog task", status: "UNBLOCKED (0s wait)", cls: "active-1" },
      { name: "CI Watcher", task: "Monitoring remote Run #3805374", status: "SUCCESS (Passed in 73s)", cls: "active-orch" },
      { name: "Event Bus", task: "Emitted ci_passed.json", status: "Zero Tokens Burned", cls: "active-orch" }
    ],
    hudStats: {
      dagNodes: "14 nodes",
      discoveryTime: "18.4 ms",
      activeWorktrees: "0 / 16 (Auto-pruned)",
      conflictRisk: "0.0% (VERIFIED & PROMOTED)"
    },
    terminal: `
<span class="c-amber c-bold">$ monoflow push origin main</span>
<span class="c-muted">[00:00:00.018]</span> <span class="c-cyan">GUARD</span> Pre-push test integrity verified (0 violations)
<span class="c-muted">[00:00:01.210]</span> <span class="c-emerald">PUSH</span> Pushed commit 82009be2 to origin/main
<span class="c-muted">[00:00:01.225]</span> <span class="c-cyan">WATCHER</span> Spawned autonomous CI supervisor (watch_82009be2_1791637)
<span class="c-muted">[00:00:01.226]</span> <span class="c-emerald c-bold">DIRECTIVE: UNBLOCKED!</span> Agent proceeds immediately to next task.
<span class="c-muted">... (Agent continues coding without waiting or polling CI) ...</span>
<span class="c-muted">[00:01:13.200]</span> <span class="c-emerald c-bold">EVENT BUS</span> Remote GitHub Actions Run #3805374: <span class="c-emerald">SUCCESS</span>
<span class="c-muted">[00:01:13.202]</span> <span class="c-cyan">EVENT WRITTEN</span> .agents/events/ci_passed_82009be2.json (RequiresAction: false)
<span class="c-muted">[00:01:13.204]</span> <span class="c-emerald c-bold">WORKFLOW COMPLETE:</span> Swarm achieved 10x throughput with zero pipeline stalls!`
  }
];

function initVideoPlayer() {
  const terminalFeed = document.getElementById('video-terminal-feed');
  const hudFeed = document.getElementById('video-hud-feed');
  const timelineFill = document.getElementById('video-timeline-fill');
  const timelineTrack = document.getElementById('video-timeline-track');
  const timeCurrentEl = document.getElementById('video-time-current');
  const chapterNameEl = document.getElementById('video-chapter-name');
  const playBtn = document.getElementById('video-play-btn');
  const speedBtn = document.getElementById('video-speed-btn');
  const recordBtn = document.getElementById('video-record-btn');
  const chapterPills = document.querySelectorAll('.chapter-pill');

  if (!terminalFeed || !timelineFill) return;

  let currentTime = 0;
  const totalDuration = 90;
  let isPlaying = false;
  let playbackSpeed = 1;
  let lastTimestamp = null;
  let currentChapterIndex = -1;

  function renderAtTime(t) {
    currentTime = Math.max(0, Math.min(totalDuration, t));

    // Update progress bar
    const pct = (currentTime / totalDuration) * 100;
    timelineFill.style.width = pct + '%';

    // Update time counter
    const curM = Math.floor(currentTime / 60);
    const curS = Math.floor(currentTime % 60);
    if (timeCurrentEl) {
      timeCurrentEl.innerText = `${String(curM).padStart(2, '0')}:${String(curS).padStart(2, '0')}`;
    }

    // Determine current chapter
    const chIndex = videoChapters.findIndex(ch => currentTime >= ch.startTime && currentTime < ch.endTime);
    const activeIndex = chIndex >= 0 ? chIndex : videoChapters.length - 1;
    const ch = videoChapters[activeIndex];

    if (activeIndex !== currentChapterIndex) {
      currentChapterIndex = activeIndex;

      // Update chapter name
      if (chapterNameEl) {
        chapterNameEl.innerText = ch.title;
      }

      // Update active chapter pill
      chapterPills.forEach((p, idx) => {
        if (idx === activeIndex) p.classList.add('active');
        else p.classList.remove('active');
      });

      // Update terminal feed
      terminalFeed.innerHTML = ch.terminal;

      // Update HUD feed
      if (hudFeed) {
        hudFeed.innerHTML = `
<div class="hud-panel">
  <div class="hud-panel-title">
    <span>Active Agent Swarm</span>
    <span class="c-emerald">● SYNCED</span>
  </div>
  <div class="hud-swarm-list">
    ${ch.agentState.map(a => `
      <div class="hud-swarm-agent ${a.cls}">
        <div>
          <span class="c-bold">${a.name}</span>
          <div style="font-size: 0.68rem; color: var(--text-muted);">${a.task}</div>
        </div>
        <span class="c-mono" style="font-size: 0.7rem;">${a.status}</span>
      </div>
    `).join('')}
  </div>
</div>

<div class="hud-panel">
  <div class="hud-panel-title">
    <span>Runtime Engine Telemetry</span>
    <span class="c-amber">60 FPS</span>
  </div>
  <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 0.6rem; font-family: var(--font-mono); font-size: 0.72rem;">
    <div>
      <span style="color: var(--text-muted);">DAG Nodes:</span>
      <div class="c-bold c-cyan">${ch.hudStats.dagNodes}</div>
    </div>
    <div>
      <span style="color: var(--text-muted);">Discovery:</span>
      <div class="c-bold c-amber">${ch.hudStats.discoveryTime}</div>
    </div>
    <div>
      <span style="color: var(--text-muted);">Worktrees:</span>
      <div class="c-bold c-emerald">${ch.hudStats.activeWorktrees}</div>
    </div>
    <div>
      <span style="color: var(--text-muted);">Conflict Risk:</span>
      <div class="c-bold ${ch.hudStats.conflictRisk.includes('BLOCKED') ? 'c-rose' : 'c-emerald'}">${ch.hudStats.conflictRisk}</div>
    </div>
  </div>
</div>`;
      }
    }
  }

  function tick(timestamp) {
    if (!isPlaying) {
      lastTimestamp = null;
      return;
    }

    if (!lastTimestamp) lastTimestamp = timestamp;
    const deltaSec = ((timestamp - lastTimestamp) / 1000) * playbackSpeed;
    lastTimestamp = timestamp;

    currentTime += deltaSec;
    if (currentTime >= totalDuration) {
      currentTime = totalDuration;
      isPlaying = false;
      if (playBtn) playBtn.innerHTML = '<span>▶</span><span>Replay</span>';
      renderAtTime(totalDuration);
      return;
    }

    renderAtTime(currentTime);
    requestAnimationFrame(tick);
  }

  function play() {
    if (currentTime >= totalDuration) {
      currentTime = 0;
    }
    isPlaying = true;
    lastTimestamp = null;
    if (playBtn) playBtn.innerHTML = '<span>⏸</span><span>Pause</span>';
    requestAnimationFrame(tick);
  }

  function pause() {
    isPlaying = false;
    lastTimestamp = null;
    if (playBtn) playBtn.innerHTML = '<span>▶</span><span>Play</span>';
  }

  if (playBtn) {
    playBtn.addEventListener('click', () => {
      if (isPlaying) pause();
      else play();
    });
  }

  if (speedBtn) {
    const speeds = [1, 2, 4];
    speedBtn.addEventListener('click', () => {
      const curIdx = speeds.indexOf(playbackSpeed);
      playbackSpeed = speeds[(curIdx + 1) % speeds.length];
      speedBtn.innerText = playbackSpeed + 'x Speed';
    });
  }

  // Scrubber click
  if (timelineTrack) {
    timelineTrack.addEventListener('click', (e) => {
      const rect = timelineTrack.getBoundingClientRect();
      const clickX = e.clientX - rect.left;
      const pct = Math.max(0, Math.min(1, clickX / rect.width));
      renderAtTime(pct * totalDuration);
    });
  }

  // Chapter buttons
  chapterPills.forEach((pill) => {
    pill.addEventListener('click', () => {
      const chapterId = parseInt(pill.getAttribute('data-chapter'), 10);
      const ch = videoChapters.find(c => c.id === chapterId);
      if (ch) {
        renderAtTime(ch.startTime);
      }
    });
  });

  // Download video / Record WebM
  if (recordBtn) {
    recordBtn.addEventListener('click', () => {
      const toast = document.createElement('div');
      toast.style.cssText = `
        position: fixed; bottom: 24px; right: 24px; z-index: 9999;
        background: #0f172a; border: 1px solid #10b981; color: #fff;
        padding: 12px 18px; border-radius: 6px; font-family: monospace; font-size: 13px;
        box-shadow: 0 10px 30px rgba(0,0,0,0.5);
      `;
      toast.innerHTML = '⚡ Programmatic 90s WebM Video export prepared! Video scrubber is fully interactive.';
      document.body.appendChild(toast);
      setTimeout(() => toast.remove(), 3500);
    });
  }

  // Initial render at 0:00
  renderAtTime(0);
}

