import os
from typing import Any, Dict
from hatchling.builders.hooks.plugin.interface import BuildHookInterface


uname_to_python_arch = {
    "Linux-x86_64": "manylinux_2_17_x86_64.manylinux2014_x86_64.musllinux_1_1_x86_64",
    "Linux-aarch64": "manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64",
    "Linux-loongarch64": "linux_loongarch64",
    "Linux-riscv64": "linux_riscv64",
    "Darwin-x86_64": "macosx_10_12_x86_64",
    "Darwin-arm64": "macosx_11_0_arm64",
}


class CustomBuildHook(BuildHookInterface):
    def initialize(self, version: str, build_data: Dict[str, Any]) -> None:
        if "TARGET" in os.environ:
            uname = os.environ["TARGET"]
            pytarget = uname_to_python_arch[uname]
        else:
            uname = f"{os.uname().sysname}-{os.uname().machine}"
            pytarget = uname_to_python_arch[uname]

        build_data["tag"] = f"py3-none-{pytarget}"
        repo_dagger_path = f"bin/repo_dagger-{uname}"
        build_data.setdefault("shared_scripts", {})[repo_dagger_path] = "repo_dagger"
