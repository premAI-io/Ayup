#!/bin/sh
# Copyright 2019 the deno authors. All rights reserved. MIT license.
# Copyright 2024 the ayup authors
# TODO(everyone): Keep this script simple and easily auditable.

set -e

if [ "$OS" = "Windows_NT" ]; then
	target="x86_64-pc-windows-msvc"
else
	case $(uname -sm) in
	"Darwin x86_64") target="darwin-amd64" ;;
	"Darwin arm64") target="darwin-arm64" ;;
	"Linux aarch64") target="linux-arm64" ;;
	*) target="linux-amd64" ;;
	esac
fi

if [ $# -eq 0 ]; then
	ayup_uri="https://github.com/premAI-io/Ayup/releases/latest/download/ay-${target}"
else
	ayup_uri="https://github.com/premAI-io/Ayup/releases/download/${1}/ay-${target}"
fi

ayup_install="${AYUP_INSTALL:-$HOME/.ayup}"
bin_dir="$ayup_install/bin"
exe="$bin_dir/ay"

if [ ! -d "$bin_dir" ]; then
	mkdir -p "$bin_dir"
fi

curl --fail --location --progress-bar --output "$exe" "$ayup_uri"
chmod +x "$exe"

echo "Ayup was installed successfully to $exe"
if command -v ay >/dev/null; then
	echo "Run 'ay --help' to get started"
else
	case $SHELL in
	/bin/zsh) shell_profile=".zshrc" ;;
	*) shell_profile=".bashrc" ;;
	esac
	echo "Manually add the directory to your \$HOME/$shell_profile (or similar)"
	echo "  export AYUP_INSTALL=\"$ayup_install\""
	echo "  export PATH=\"\$ayup_INSTALL/bin:\$PATH\""
	echo "Run '$exe --help' to get started"
fi
echo
echo "Stuck? Talk to richard@premai.io"
