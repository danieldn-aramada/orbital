#!/usr/bin/env bash
# Build and ship a new orbital CLI release.
#
# Builds darwin/arm64 + darwin/amd64 binaries, creates a GitHub release on
# the orbital source repo tagged as cli/<version>, then commits an updated
# Homebrew formula to the tap repo. macOS-only for now.
#
# CLI is versioned independently from the orbital server. Tags follow the
# cli/v* convention so they don't collide with the server's bare v* lineage.
#
# Prereqs: gh CLI authed (`gh auth status` must succeed), clean git tree.
# Usage:   ./scripts/release-cli.sh v0.0.1
#          (pass just the version; cli/ prefix is added automatically)

set -euo pipefail

VERSION="${1:?usage: release-cli.sh v0.0.1}"
TAG="cli/${VERSION}"
TAP_REPO="danieldn-aramada/homebrew-tools"
SRC_REPO="danieldn-aramada/orbital"

# 1. Build Mac binaries. orbital-cli is pure Go — no CGo, trivial cross-compile.
rm -rf dist && mkdir -p dist

LDFLAGS="-s -w -X github.com/armada/orbital/internal/version.Version=$VERSION -X 'github.com/armada/orbital/internal/orbital-cli.DefaultServerURL=http://ilb.devnew.armada.internal/orbital'"

build_one() {
  local arch="$1"
  GOOS=darwin GOARCH=$arch go build -ldflags "$LDFLAGS" -o dist/orbital ./cmd/orbital-cli
  tar -C dist -czf "dist/orbital_${VERSION}_darwin_${arch}.tar.gz" orbital
  rm dist/orbital
}

build_one arm64
build_one amd64

SHA_ARM=$(shasum -a 256 "dist/orbital_${VERSION}_darwin_arm64.tar.gz" | awk '{print $1}')
SHA_AMD=$(shasum -a 256 "dist/orbital_${VERSION}_darwin_amd64.tar.gz" | awk '{print $1}')

# 2. Tag (cli/v...) and create GitHub release with both tarballs
git tag "$TAG"
git push origin "$TAG"
gh release create "$TAG" \
  --repo "$SRC_REPO" \
  --title "$TAG" \
  --notes "orbital CLI $VERSION" \
  "dist/orbital_${VERSION}_darwin_arm64.tar.gz" \
  "dist/orbital_${VERSION}_darwin_amd64.tar.gz"

# 3. Generate the formula. Release-download URLs include the cli/ tag segment.
cat > /tmp/orbital.rb <<EOF
class Orbital < Formula
  desc "Orbital CLI — authenticate and interact with the Orbital cloud service"
  homepage "https://github.com/${SRC_REPO}"
  version "${VERSION#v}"

  if Hardware::CPU.arm?
    url "https://github.com/${SRC_REPO}/releases/download/${TAG}/orbital_${VERSION}_darwin_arm64.tar.gz"
    sha256 "${SHA_ARM}"
  else
    url "https://github.com/${SRC_REPO}/releases/download/${TAG}/orbital_${VERSION}_darwin_amd64.tar.gz"
    sha256 "${SHA_AMD}"
  end

  def install
    bin.install "orbital"
    generate_completions_from_executable(bin/"orbital", "completion")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/orbital version")
  end
end
EOF

# 4. Commit + push the formula to the tap repo
TMP=$(mktemp -d)
gh repo clone "$TAP_REPO" "$TMP/tap"
cp /tmp/orbital.rb "$TMP/tap/Formula/orbital.rb"
( cd "$TMP/tap" && git add Formula/orbital.rb && git commit -m "orbital ${VERSION}" && git push )
rm -rf "$TMP"

echo ""
echo "✓ Released ${TAG}"
echo ""
echo "Devs can now run:"
echo "  brew tap danieldn-aramada/tools"
echo "  brew install orbital"
