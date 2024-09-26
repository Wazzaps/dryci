import pytest

from example_service.module2 import other_greet


@pytest.mark.parametrize(
    "name, expected",
    [("world", "Howdy, world!"), ("Alice", "Howdy, Alice!"), ("Bob", "Howdy, Bob!")],
)
def test_other_greet(name, expected):
    assert other_greet(name) == expected
