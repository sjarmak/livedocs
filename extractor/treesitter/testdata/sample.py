"""Sample Python module for testing."""

import os
from pathlib import Path


class Greeter:
    """A class that greets people."""

    def greet(self, name: str) -> str:
        """Return a greeting for name."""
        return f"Hello, {name}!"


def main():
    """Entry point."""
    g = Greeter()
    print(g.greet("world"))


# A constant
DEFAULT_TIMEOUT = 30
