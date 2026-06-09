#!/usr/bin/env bash
# Build and ship a new orbital CLI release.
#
# Builds darwin/arm64 + darwin/amd64 binaries, creates a GitHub release on
# the orbital source repo, then commits an updated Homebrew formula to the
# tap repo. macOS-only for now.
#
# Prereqs: gh CLI authed (`gh auth status` must succeed), clean git tree.
# Usage:   ./scripts/release-cli.sh v0.0.1

set -euo pipefail

VERSION="${1:?usage: release-cli.sh v0.0.1}"
TAP_REPO="danieldn-aramada/homebrew-tools"
SRC_REPO="danieldn-aramada/orbital"

# 1. Build Mac binaries. CGO is required for macOS keychain access.
# amd64 cross-compile from arm64 host uses `clang -arch x86_64` (Xcode-provided).
rm -rf dist && mkdir -p dist

LDFLAGS="-s -w -X github.com/armada/orbital/internal/version.Version=$VERSION"

build_one() {
  local arch="$1"
  if [ "$arch" = "amd64" ]; then
    CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
      CC="clang -arch x86_64" \
      CXX="clang++ -arch x86_64" \
      go build -ldflags "$LDFLAGS" -o dist/orbital ./cmd/orbital-cli
  else
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
      go build -ldflags "$LDFLAGS" -o dist/orbital ./cmd/orbital-cli
  fi
  tar -C dist -czf "dist/orbital_${VERSION}_darwin_${arch}.tar.gz" orbital
  rm dist/orbital
}

build_one arm64
build_one amd64

SHA_ARM=$(shasum -a 256 "dist/orbital_${VERSION}_darwin_arm64.tar.gz" | awk '{print $1}')
SHA_AMD=$(shasum -a 256 "dist/orbital_${VERSION}_darwin_amd64.tar.gz" | awk '{print $1}')

# 2. Tag and create GitHub release with both tarballs
git tag "$VERSION"
git push origin "$VERSION"
gh release create "$VERSION" \
  --repo "$SRC_REPO" \
  --title "$VERSION" \
  --notes "orbital CLI $VERSION" \
  "dist/orbital_${VERSION}_darwin_arm64.tar.gz" \
  "dist/orbital_${VERSION}_darwin_amd64.tar.gz"

# 3. Generate the formula
cat > /tmp/orbital.rb <<EOF
class Orbital < Formula
  desc "Orbital CLI — authenticate and interact with the Orbital cloud service"
  homepage "https://github.com/${SRC_REPO}"
  version "${VERSION#v}"

  if Hardware::CPU.arm?
    url "https://github.com/${SRC_REPO}/releases/download/${VERSION}/orbital_${VERSION}_darwin_arm64.tar.gz"
    sha256 "${SHA_ARM}"
  else
    url "https://github.com/${SRC_REPO}/releases/download/${VERSION}/orbital_${VERSION}_darwin_amd64.tar.gz"
    sha256 "${SHA_AMD}"
  end

  def install
    bin.install "orbital"
  end

  test do
    system "#{bin}/orbital", "--version"
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
echo "✓ Released ${VERSION}"
echo ""
echo "Devs can now run:"
echo "  brew tap danieldn-aramada/tools"
echo "  brew install orbital"
