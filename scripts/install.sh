#!/bin/sh
set -eu

repository="igorrochap/rig"
download_base="${RIG_DOWNLOAD_BASE:-https://github.com/$repository/releases}"
install_dir="${INSTALL_DIR:-${HOME}/.local/bin}"
version="latest"

usage() {
  echo "Usage: $0 [-d|--dir INSTALL_DIR] [-v|--version VERSION]" >&2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    -d|--dir)
      if [ "$#" -lt 2 ]; then
        usage
        exit 1
      fi
      install_dir="$2"
      shift 2
      ;;
    -v|--version)
      if [ "$#" -lt 2 ]; then
        usage
        exit 1
      fi
      version="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 1
      ;;
  esac
done

for command_name in curl tar; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "Error: $command_name is required." >&2
    exit 1
  fi
done

case "$(uname -s)" in
  Linux) os="Linux" ;;
  Darwin) os="Darwin" ;;
  *)
    echo "Error: unsupported operating system: $(uname -s)" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "Error: unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

asset="rig_${os}_${arch}.tar.gz"
if [ "$version" = "latest" ]; then
  release_url="$download_base/latest/download"
else
  case "$version" in
    v*) ;;
    *) version="v$version" ;;
  esac
  release_url="$download_base/download/$version"
fi

temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/rig-install.XXXXXX")"
cleanup() {
  rm -rf "$temp_dir"
}
trap cleanup EXIT HUP INT TERM

echo "Downloading rig ${version} for ${os}/${arch}..."
curl --fail --location --silent --show-error \
  --output "$temp_dir/$asset" "$release_url/$asset"
curl --fail --location --silent --show-error \
  --output "$temp_dir/checksums.txt" "$release_url/checksums.txt"

expected_checksum="$(awk -v asset="$asset" '$2 == asset { print $1; exit }' "$temp_dir/checksums.txt")"
if [ -z "$expected_checksum" ]; then
  echo "Error: checksum for $asset was not found." >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum="$(sha256sum "$temp_dir/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual_checksum="$(shasum -a 256 "$temp_dir/$asset" | awk '{print $1}')"
else
  echo "Error: sha256sum or shasum is required." >&2
  exit 1
fi

if [ "$actual_checksum" != "$expected_checksum" ]; then
  echo "Error: checksum verification failed for $asset." >&2
  exit 1
fi

mkdir -p "$temp_dir/extracted" "$install_dir"
tar -xzf "$temp_dir/$asset" -C "$temp_dir/extracted"

if [ ! -f "$temp_dir/extracted/rig" ]; then
  echo "Error: release archive does not contain rig." >&2
  exit 1
fi
install -m 0755 "$temp_dir/extracted/rig" "$install_dir/rig"

echo "Installed rig to $install_dir."
case ":${PATH}:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to PATH to run rig from any directory." ;;
esac
