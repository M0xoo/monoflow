document.addEventListener('DOMContentLoaded', () => {
  // Theme Toggle
  const themeToggleBtn = document.getElementById('theme-toggle');
  const currentTheme = localStorage.getItem('monoflow-theme') || 'dark';
  document.documentElement.setAttribute('data-theme', currentTheme);
  updateThemeIcon(currentTheme);

  if (themeToggleBtn) {
    themeToggleBtn.addEventListener('click', () => {
      const theme = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
      document.documentElement.setAttribute('data-theme', theme);
      localStorage.setItem('monoflow-theme', theme);
      updateThemeIcon(theme);
    });
  }

  function updateThemeIcon(theme) {
    if (themeToggleBtn) {
      themeToggleBtn.textContent = theme === 'dark' ? '☀' : '☾';
    }
  }

  // Mobile Menu Toggle
  const mobileMenuBtn = document.getElementById('mobile-menu-btn');
  const sidebar = document.querySelector('.sidebar');
  if (mobileMenuBtn && sidebar) {
    mobileMenuBtn.addEventListener('click', () => {
      sidebar.classList.toggle('open');
    });
  }

  // Copy Buttons
  document.querySelectorAll('.copy-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const targetId = btn.getAttribute('data-target');
      let textToCopy = '';
      if (targetId) {
        const targetEl = document.getElementById(targetId);
        textToCopy = targetEl ? targetEl.textContent : '';
      } else {
        const codeEl = btn.closest('.code-container')?.querySelector('code');
        textToCopy = codeEl ? codeEl.textContent : btn.previousElementSibling?.textContent || '';
      }

      if (textToCopy) {
        navigator.clipboard.writeText(textToCopy.trim()).then(() => {
          const originalText = btn.textContent;
          btn.textContent = 'Copied';
          setTimeout(() => {
            btn.textContent = originalText;
          }, 1800);
        });
      }
    });
  });

  // Search Filter
  const searchInput = document.getElementById('search-input');
  if (searchInput) {
    searchInput.addEventListener('input', (e) => {
      const query = e.target.value.toLowerCase().trim();
      const sidebarLinks = document.querySelectorAll('.sidebar-link');

      sidebarLinks.forEach(link => {
        const text = link.textContent.toLowerCase();
        if (text.includes(query) || !query) {
          link.style.display = 'flex';
        } else {
          link.style.display = 'none';
        }
      });
    });

    window.addEventListener('keydown', (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault();
        searchInput.focus();
      }
    });
  }

  // Scroll Spy for Sidebar Active Link
  const sections = document.querySelectorAll('section[id]');
  window.addEventListener('scroll', () => {
    let current = '';
    sections.forEach(section => {
      const sectionTop = section.offsetTop - 100;
      if (window.pageYOffset >= sectionTop) {
        current = section.getAttribute('id');
      }
    });

    document.querySelectorAll('.sidebar-link').forEach(link => {
      link.classList.remove('active');
      if (link.getAttribute('href') === `#${current}`) {
        link.classList.add('active');
      }
    });
  });

  // Interactive Blueprint Inspector
  const blueprintCanvas = document.getElementById('blueprint-canvas');
  const segmentedBtns = document.querySelectorAll('.segmented-btn');

  const models = {
    ast: `// Same-File Multi-Agent AST Synthesis (packages/shared-go/logger/logger.go)
// Agent Alpha added LogAuthSecurityEvent() in worktree alpha
// Agent Beta added LogBillingTransaction() in worktree beta

$ monoflow worktree sync --to=agent-alpha --from=agent-beta

{
  "status": "synced",
  "from_agent": "agent-beta",
  "to_agent": "agent-alpha",
  "synced_files": [
    "packages/shared-go/logger/logger.go (synthesized AST)"
  ],
  "duration_ms": 25.766,
  "verdict": "AST parsed, declarations united, imports deduplicated. Zero conflict markers."
}`,

    audit: `// PR Intent & Scope Auditor (Catching undeclared database migration)
// Agent PR claims: "docs: update readme" but modified SQL migration in git

$ monoflow review audit --title="docs: update readme" --desc="minor doc fixes"

{
  "valid": false,
  "undeclared_files": [
    "packages/database-schemas/migrations/003_stripe_customer.sql"
  ],
  "hallucination_warnings": [
    "PR claims trivial change ('docs: update readme') but modifies non-doc file: jwt.go",
    "Undisclosed critical blast radius: 003_stripe_customer.sql modified in Git but omitted from PR description"
  ],
  "verdict": "PR intent audit failed: unmentioned critical files or misleading claims detected"
}`,

    queue: `// Speculative Local Merge Queue (Bypassing quarantined flaky tests)
// Simulates landing on main in 36ms before hitting remote CI runners

$ monoflow queue spec task-enterprise-007

{
  "task_id": "task-enterprise-007",
  "speculative_merge_sha": "37bc26d587aa3e51e456a51125ddfc54a7b9bbced5d1ab8587aba299fbfbd84c",
  "affected_packages": ["services/billing-service"],
  "bypassed_quarantined_tests": [
    "test_stripe_webhook_timeout"
  ],
  "duration_ms": 36.729,
  "status": "speculatively_verified",
  "signature": "32ccd419d1817687bfa0083ce879cd0b6c5bcd2692e2227a27abe56ea3d148e1",
  "verdict": "Eligible for GitHub Actions Tier 1 Fast-Path Merge (< 30s)"
}`,

    pow: `// 1-Second Reviewer Verification via Cryptographic Proof Receipt
// Replaces 15-minute human checkout and rebuild cycle

$ monoflow verify --proof=task-billing-discount

{
  "status": "VERIFIED",
  "task_id": "task-billing-discount",
  "verified_in_ms": 0.159,
  "dod_checks_passed": 4,
  "git_tree_sha": "2e90bad1e5030e3e0d10f6a314eaa2946ff487da",
  "deliverable_sha": "8221286bcc8982acc0dd5827536a32d6a63e5a19cf6782c55118371344c42f0e",
  "confidence_verdict": "APPROVED (1-Second Instant Verification Passed)"
}`
  };

  segmentedBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      segmentedBtns.forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      const key = btn.getAttribute('data-model');
      if (models[key] && blueprintCanvas) {
        blueprintCanvas.textContent = models[key];
      }
    });
  });

  // Default trigger
  const defaultBtn = document.querySelector('.segmented-btn[data-model="ast"]');
  if (defaultBtn) {
    defaultBtn.click();
  }
});
