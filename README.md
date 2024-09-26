# DryCI - Test caching system

This pytest plugin speeds up test runs by caching test results. It calculates a hash of the contents of all files that might affect the test results, and looks up the result in a central server.

This repository contains both the [pytest plugin](./pytest_dryci) and a reference [server](./dryci_server).

This solution is a lightweight alternative to the test-caching part of heavy hermetic build systems like Bazel or Buck. It is designed to be easy to set up and use, and to work with existing projects without requiring full-time engineers to manage it.

User documentation, a public cache server, a hermeticity verifier, and more language support might come in the future.

## How to use

1. Add the `pytest-dryci` package to your dev-dependencies.
2. Configure `repo_dagger.yaml`, see [example_project](./pytest_dryci/example_project) for inspiration.
3. Obtain a DryCI token for a DryCI server. The public instance is currently in closed beta, but you may deploy your own.
4. Run `DRYCI_TOKEN=$your_token_here pytest --dryci ./tests`.

You may also specify DRYCI_SERVER to use a custom server, DRYCI_SALT to do cache-busting, and DRYCI_TIMEOUT to control http timeouts.

## How it works

The plugin runs [`repo_dagger`](https://github.com/Wazzaps/repo_dagger) to analyze the runtime dependencies of all files that might affect the test results. To do this effectively, you will need to configure rules that define dependencies between files in `repo_dagger.yaml`. See [example_config.toml](https://github.com/Wazzaps/repo_dagger/blob/main/example_config.yaml) for an example config.

<img src="./docs/img/readme_1.png" width="640" alt="A dependency graph between project files"/>

Then, for each file in the test suite, it traverses the dependency graph and hashes each file. The hash of all these files is known as the "dependency hash" of the test file. The hash also includes some info about the system, such as the operating system, distro, architecture, and the contents of the `DRYCI_SALT` environment variable.

<img src="./docs/img/readme_2.png" width="640" alt="A hash function hashing the dependencies from the graph above"/>

It then queries the server for the list of passed tests for each dependency hash, and skips any tests it returns. Test identifiers (pytest `nodeids`) are salted and hashed for privacy.

Note that failed tests are never cached, so flaky tests get a chance to run again. Skipped tests are also not cached, but that might change in the future.

<img src="./docs/img/readme_3.png" width="640" alt="A computer queries the server what tests need to run, and which should be skipped"/>

After running the test suite, we upload the (hashed) identifier of each test that passed, so future runs can skip them.

<img src="./docs/img/readme_4.png" width="640" alt="A computer uploads hashes of passed tests to the server"/>
