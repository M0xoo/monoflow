#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# MonoFlow Installer: Agent-Native Operating Environment for Monorepos
# ==============================================================================

echo "⚡ Installing MonoFlow into current repository..."

# 1. Verify Git Repository
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "❌ Error: Current directory is not inside a Git repository."
    echo "Please run this command from the root of your repository."
    exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
AGENTS_DIR="${REPO_ROOT}/.agents"
BIN_DIR="${AGENTS_DIR}/bin"

mkdir -p "${BIN_DIR}"

# 2. Locate or Build MonoFlow binary
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MONOFLOW_SRC_BIN="${SCRIPT_DIR}/bin/monoflow"

if [[ -f "${MONOFLOW_SRC_BIN}" ]]; then
    cp "${MONOFLOW_SRC_BIN}" "${BIN_DIR}/monoflow"
elif command -v monoflow >/dev/null 2>&1; then
    cp "$(command -v monoflow)" "${BIN_DIR}/monoflow"
elif command -v go >/dev/null 2>&1 && [[ -f "${SCRIPT_DIR}/cmd/monoflow/main.go" ]]; then
    echo "🔨 Compiling MonoFlow binary from source..."
    (cd "${SCRIPT_DIR}" && go build -o "${BIN_DIR}/monoflow" ./cmd/monoflow)
else
    # In production, download pre-built binary for OS/arch (darwin-arm64, darwin-amd64, linux-amd64)
    echo "⬇️  Downloading pre-built MonoFlow binary..."
    OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"
    if [[ "${ARCH}" == "x86_64" ]]; then ARCH="amd64"; fi
    if [[ "${ARCH}" == "aarch64" ]]; then ARCH="arm64"; fi
    # Latest release by default; pin with MONOFLOW_VERSION=vX.Y.Z.
    URL="https://github.com/M0xoo/monoflow/releases/latest/download/monoflow-${OS}-${ARCH}"
    if [[ -n "${MONOFLOW_VERSION:-}" ]]; then
        URL="https://github.com/M0xoo/monoflow/releases/download/${MONOFLOW_VERSION}/monoflow-${OS}-${ARCH}"
    fi
    if ! curl -fsSL "${URL}" -o "${BIN_DIR}/monoflow"; then
        echo "❌ Unable to download ${URL} (no release for ${OS}/${ARCH}?). Install Go and re-run from a MonoFlow checkout to build from source."
        exit 1
    fi
fi

chmod +x "${BIN_DIR}/monoflow"

# 3. Run MonoFlow Auto-Discovery & Initialization
echo "🔍 Auto-discovering repository architecture..."
"${BIN_DIR}/monoflow" init "${REPO_ROOT}"

echo ""
echo "=================================================================="
echo "🎉 MonoFlow Successfully Installed!"
echo "=================================================================="
echo "📁 Agent Operating Environment: ${AGENTS_DIR}"
echo "⚡ Binary: ${BIN_DIR}/monoflow"
echo "📜 Rules:  ${AGENTS_DIR}/rules/agentic-monorepo.md"
echo "🧠 Skills: ${AGENTS_DIR}/skills/monoflow/SKILL.md"
echo "⚙️  Config: ${AGENTS_DIR}/monoflow.config.json"
echo ""
echo "🤖 Autonomous Agents (Antigravity, Cursor, Claude Code) will now:"
echo "   • Query dependencies via '.agents/bin/monoflow graph query'"
echo "   • Take 15ms snapshots via '.agents/bin/monoflow checkpoint save'"
echo "   • Run sub-2s inner-loop CI via '.agents/bin/monoflow verify --affected'"
echo "   • Validate DoD & Proof of Work via '.agents/bin/monoflow task prove'"
echo "=================================================================="
