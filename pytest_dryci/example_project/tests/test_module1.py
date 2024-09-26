import pytest

from example_service.module1 import simple_greet


@pytest.mark.parametrize(
    "name, expected",
    [("world", "Hello, world!"), ("Alice", "Hello, Alice!"), ("Bob", "Hello, Bob!")],
)
def test_simple_greet(name, expected):
    assert simple_greet(name) == expected
