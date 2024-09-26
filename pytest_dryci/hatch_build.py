import os
from typing import Any, Dict
from hatchling.builders.hooks.plugin.interface import BuildHookInterface


python_arch_to_uname = {
    "linux_x86_64": "Linux-x86_64",
    "linux_aarch64": "Linux-aarch64",
    "linux_loongarch64": "Linux-loongarch64",
    "linux_riscv64": "Linux-riscv64",
    "macosx_10_12_x86_64": "Darwin-x86_64",
    "macosx_11_0_arm64": "Darwin-arm64",
}
uname_to_python_arch = {v: k for k, v in python_arch_to_uname.items()}


class CustomBuildHook(BuildHookInterface):
    def initialize(self, version: str, build_data: Dict[str, Any]) -> None:
        if "TARGET" in os.environ:
            target = os.environ["TARGET"]
            uname = python_arch_to_uname[target]
        else:
            uname = f"{os.uname().sysname}-{os.uname().machine}"
            target = uname_to_python_arch[uname]

        build_data["tag"] = f"py3-none-{target}"
        repo_dagger_path = f"bin/repo_dagger-{uname}"
        build_data.setdefault("shared_scripts", {})[repo_dagger_path] = "repo_dagger"
