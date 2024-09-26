import atexit
import hashlib
import json
import os
from pathlib import Path
import subprocess
import threading
from typing import Dict, List, Optional, Tuple
import urllib.request

import pytest

_TEST_HASHES_INITIALIZED = False

_passed_key = pytest.StashKey[bool]()
_failed_key = pytest.StashKey[bool]()
_skipped_key = pytest.StashKey[bool]()
_skipped_by_cache_key = pytest.StashKey[bool]()
_test_file_hash_key = pytest.StashKey[str]()
_hashed_nodeid_key = pytest.StashKey[str]()


def pytest_addoption(parser):
    parser.addoption(
        "--dryci",
        action="store_true",
        help="Enable DryCI test caching (requires DRYCI_TOKEN environment variable)",
    )
    parser.addoption(
        "--dryci-no-publish",
        action="store_true",
        help="Do not publish test results to DryCI",
    )
    parser.addoption(
        "--dryci-no-skip",
        action="store_true",
        help="Do not skip tests based on DryCI",
    )


def dryci_api_request(endpoint, data):
    # TODO: Add retries
    # TODO: Make sure it uses system proxy & certificates
    server = os.environ.get("DRYCI_SERVER", "https://dryci.wazzaps.net")
    req = urllib.request.Request(
        server + endpoint,
        data=json.dumps(data).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {os.environ['DRYCI_TOKEN']}",
            "Content-Type": "application/json",
        },
    )
    with urllib.request.urlopen(req, timeout=int(os.environ.get("DRYCI_TIMEOUT", 3))) as response:
        if response.status != 200:
            print(
                f"[DryCI Error: request failed with code {response.status}. "
                "disabling test caching]"
            )
            return
        return json.loads(response.read())


def _hash_nodeid(item, salt: str) -> bytes:
    return hashlib.blake2s(
        (item.nodeid + salt).encode("utf-8"),
        # Probability of collision: P(2**128, TESTS_PER_FILE).
        # Assuming a worst case of TESTS_PER_FILE = 2**16, this is roughly 10**(-30).
        digest_size=16,
    ).digest()


def skip_cached_tests(items, config):
    global _TEST_HASHES_INITIALIZED

    try:
        if not config.option.dryci:
            return
        if "DRYCI_TOKEN" not in os.environ:
            print("[DryCI Error: DRYCI_TOKEN not provided. disabling test caching]")
            return

        # Collect all test paths
        if not items:
            return
        paths = set()
        for item in items:
            paths.add(item.path.relative_to(Path.cwd()).as_posix())
        paths = sorted(paths)

        # Calculate test hashes using repo_dagger
        test_file_hashes = run_repo_dagger(paths)
        if not test_file_hashes:
            return

        for item in items:
            test_path = item.path.relative_to(Path.cwd()).as_posix()
            hashed_nodeid = _hash_nodeid(item, test_file_hashes[test_path][0]).hex()
            item.stash[_test_file_hash_key] = test_file_hashes[test_path][1]
            item.stash[_hashed_nodeid_key] = hashed_nodeid

        _TEST_HASHES_INITIALIZED = True
        if config.option.dryci_no_skip:
            return

        # Get successful test nodes from DryCI for each test path
        queries_paths = []
        queries_hashes = []
        missing_paths = set()
        for path in paths:
            if path in test_file_hashes:
                queries_paths.append(path)
                queries_hashes.append(test_file_hashes[path][1])
            else:
                missing_paths.add(path)

        passed_tests = dryci_api_request(
            "/api/v1/query-passed", {"test_file_hashes": queries_hashes}
        )
        if passed_tests is None or "node_ids" not in passed_tests:
            return

        cached_nodes_by_path = dict(zip(queries_paths, passed_tests["node_ids"]))

        # Skip tests that have passed in the past
        skipped_count = 0
        for item in items:
            test_path = item.path.relative_to(Path.cwd()).as_posix()
            if item.stash[_hashed_nodeid_key] in cached_nodes_by_path[test_path]:
                item.add_marker(pytest.mark.skip(reason="Cached successful test"))
                item.stash[_skipped_by_cache_key] = True
                skipped_count += 1
        print(f"[DryCI: Skipping {skipped_count} tests based on cache]")
    except Exception as e:
        print(
            f"[DryCI Error: Failed while retrieving cached tests: {e!r}. disabling test caching]"
        )


def run_repo_dagger(file_paths: List[str]) -> Optional[Dict[str, Tuple[str, str]]]:
    try:
        # Include info about the environment as a salt for the hashes
        os_info = os.uname()
        try:
            distro_name = subprocess.check_output(["lsb_release", "-sd"]).decode().strip()
        except (FileNotFoundError, subprocess.CalledProcessError):
            distro_name = "Unknown"
        hash_salt = (
            f"{os_info.sysname}:{os_info.machine}:{distro_name}:{os.environ.get('DRYCI_SALT', '')}"
        )

        # Calculate test hashes using repo_dagger
        try:
            dep_hashes_fd = os.memfd_create("dep-hashes")
        except AttributeError:
            # Fallback for older Python versions
            atexit.register(lambda: os.remove("/dev/shm/dryci-dep-hashes"))
            dep_hashes_fd = os.open("/dev/shm/dryci-dep-hashes", os.O_RDWR | os.O_CREAT)

        with os.fdopen(dep_hashes_fd, "r") as dep_hashes_f:
            try:
                subprocess.check_call(
                    [
                        "repo_dagger",
                        "-config",
                        Path.cwd() / "repo_dagger.yaml",
                        "-out-dep-hashes",
                        f"/dev/fd/{dep_hashes_fd}",
                        "-hash-salt",
                        hash_salt,
                        "-input-files",
                        ",".join(file_paths),
                    ],
                    pass_fds=[dep_hashes_fd],
                )
            except FileNotFoundError:
                print("[DryCI Error: `repo_dagger` executable not found. disabling test caching]")
                return None
            except subprocess.CalledProcessError:
                print("[DryCI Error: `repo_dagger` failed. disabling test caching]")
                return None

            test_file_dep_hashes = json.load(dep_hashes_f)
            # For each hash, hash it again so we can use the original hash as a secret for
            # anonymizing the node ids,
            test_file_hashes_twice = {
                test_file: (dep_hash, hashlib.blake2s(dep_hash.encode("utf-8")).hexdigest())
                for test_file, dep_hash in test_file_dep_hashes.items()
            }
            return test_file_hashes_twice
    except Exception as e:
        print(
            f"[DryCI Error: Failed while calculating test file dependencies: {e!r}. disabling test caching]"
        )
        return None


def pytest_collection_modifyitems(items, config):
    skip_cached_tests(items, config)


@pytest.hookimpl(tryfirst=True, hookwrapper=True)
def pytest_runtest_makereport(item, call):
    result = (yield).get_result()
    # Store whether the test passed on the test item object. All test stages must pass for the
    # test to be considered successful.
    if result.when == "call":
        item.stash[_passed_key] = item.stash.get(_passed_key, True) and result.passed
    item.stash[_failed_key] = item.stash.get(_failed_key, False) or result.failed
    item.stash[_skipped_key] = item.stash.get(_skipped_key, False) or result.skipped
    return result


@pytest.hookimpl(tryfirst=True, hookwrapper=True)
def pytest_runtestloop(session):
    result = yield
    if (
        session.config.option.dryci
        and not session.config.option.dryci_no_publish
        and _TEST_HASHES_INITIALIZED
    ):
        try:
            passed_test_count = 0
            failed_test_count = 0
            skipped_test_count = 0
            skipped_by_cache_test_count = 0

            tests_to_publish = {}
            for item in session.items:
                if item.stash.get(_passed_key, False):
                    tests_to_publish.setdefault(item.stash[_test_file_hash_key], []).append(
                        item.stash[_hashed_nodeid_key]
                    )
                    passed_test_count += 1
                if item.stash.get(_failed_key, False):
                    failed_test_count += 1
                if item.stash.get(_skipped_key, False):
                    skipped_test_count += 1
                if item.stash.get(_skipped_by_cache_key, False):
                    skipped_by_cache_test_count += 1

            dryci_api_request(
                "/api/v1/publish",
                {
                    "passed_node_ids_per_test_file": tests_to_publish,
                    "total_test_count": len(session.items),
                    "passed_test_count": passed_test_count,
                    "failed_test_count": failed_test_count,
                    "skipped_test_count": skipped_test_count,
                    "skipped_by_cache_test_count": skipped_by_cache_test_count,
                },
            )
            print(f"\n[DryCI: Published {passed_test_count} passing test results]")
        except Exception as e:
            print(f"[DryCI Error: Failed while publishing test results: {e!r}]")

    return result
