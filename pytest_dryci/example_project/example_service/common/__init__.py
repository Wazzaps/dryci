import time


def compose_greeting(greeting: str, name: str) -> str:
    # Simulate a slow function
    time.sleep(1)

    return f"{greeting}, {name}!"
