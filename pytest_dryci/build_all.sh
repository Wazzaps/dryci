#!/bin/sh
set -e

echo "Building wheels for all platforms"
# Linux-loongarch64 and Linux-riscv64 are not supported by pypi for some reason
for target in Linux-x86_64 Linux-aarch64 Darwin-x86_64 Darwin-arm64; do
    TARGET=$target uv build --wheel
done