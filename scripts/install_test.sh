#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

case "$(uname -s)" in
  Linux) test_os="Linux" ;;
  Darwin) test_os="Darwin" ;;
  *) echo "unsupported test OS" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) test_arch="amd64" ;;
  arm64|aarch64) test_arch="arm64" ;;
  *) echo "unsupported test architecture" >&2; exit 1 ;;
esac

asset="rig_${test_os}_${test_arch}.tar.gz"
release_dir="$test_root/releases/latest/download"
package_dir="$test_root/package"
install_dir="$test_root/bin"
mkdir -p "$release_dir" "$package_dir"

printf '#!/bin/sh\necho rig-test\n' >"$package_dir/rig"
chmod +x "$package_dir/rig"
tar -C "$package_dir" -czf "$release_dir/$asset" rig

if command -v sha256sum >/dev/null 2>&1; then
  checksum="$(sha256sum "$release_dir/$asset" | awk '{print $1}')"
else
  checksum="$(shasum -a 256 "$release_dir/$asset" | awk '{print $1}')"
fi
printf '%s  %s\n' "$checksum" "$asset" >"$release_dir/checksums.txt"

RIG_DOWNLOAD_BASE="file://$test_root/releases" \
  "$repo_root/scripts/install.sh" --dir "$install_dir"

test "$("$install_dir/rig")" = "rig-test"

echo "installer test passed"
