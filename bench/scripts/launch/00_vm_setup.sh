#!/usr/bin/env bash
# One-time setup of a fresh Ubuntu 24.04 VM (x86_64) for the launch study. Run it as a user with sudo, from a clone
# of this repository:
#     bash bench/scripts/launch/00_vm_setup.sh
# It installs Docker, Go (the version in go.mod) and the bench's Python package, then checks each one.
# Afterwards log out and back in once, so your user can talk to Docker without sudo.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

GO_VERSION="$(sed -n 's/^go \([0-9.]*\)$/\1/p' go.mod)"
[ -n "$GO_VERSION" ] || { echo "STOP: could not read the Go version from go.mod" >&2; exit 1; }

echo "== apt packages: docker, git, python"
sudo apt-get update -q
sudo apt-get install -y -q docker.io git python3 python3-pip python3-venv curl ca-certificates
sudo usermod -aG docker "$USER"

if ! command -v go >/dev/null || [ "$(go env GOVERSION 2>/dev/null)" != "go$GO_VERSION" ]; then
  echo "== Go $GO_VERSION (from go.dev)"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz
  grep -q '/usr/local/go/bin' ~/.profile || echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.profile
  export PATH=/usr/local/go/bin:$PATH
fi

echo "== Python environment for the bench (.venv-bench)"
python3 -m venv .venv-bench
. .venv-bench/bin/activate
pip install -q -e "bench[report,dev]"
grep -q 'venv-bench' ~/.profile || echo "[ -f $PWD/.venv-bench/bin/activate ] && . $PWD/.venv-bench/bin/activate" >> ~/.profile

echo
go version
python --version
sudo docker info --format 'Docker server {{.ServerVersion}}'
echo
echo "Done. Log out and back in (for the docker group and PATH), then run:"
echo "    export ANTHROPIC_API_KEY=...      # your own pay-per-use API key"
echo "    bash bench/scripts/launch/01_check.sh"
