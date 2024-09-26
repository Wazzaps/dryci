#!/bin/sh
set -e

echo "Building wheels for all platforms"
for target in linux_x86_64 linux_aarch64 linux_loongarch64 linux_riscv64 macosx_10_12_x86_64 macosx_11_0_arm64; do
    TARGET=$target uv build --wheel
done