"""Small source module for an isolated Magi development exercise."""

def format_elapsed(milliseconds):
    """Show a non-negative integer duration in milliseconds."""
    if isinstance(milliseconds, bool) or not isinstance(milliseconds, int):
        raise TypeError("milliseconds must be an integer")
    if milliseconds < 0:
        raise ValueError("milliseconds must not be negative")
    return f"{milliseconds} ms"
