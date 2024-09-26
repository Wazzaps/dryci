from pathlib import Path
from example_service.common import compose_greeting

DATAFILE_PATH = Path(__file__).parent / "data_file.txt"


def other_greet(name: str) -> str:
    return compose_greeting(DATAFILE_PATH.read_text(), name)


if __name__ == "__main__":
    print(other_greet("world"))
