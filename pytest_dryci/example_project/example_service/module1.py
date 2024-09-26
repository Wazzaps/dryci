from example_service.common import compose_greeting


def simple_greet(name: str) -> str:
    return compose_greeting("Hello", name)


if __name__ == "__main__":
    print(simple_greet("world"))
