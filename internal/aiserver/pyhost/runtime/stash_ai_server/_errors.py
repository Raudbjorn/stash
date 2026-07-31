"""Shared errors for unsupported compatibility surfaces."""


class UnsupportedInProcess(RuntimeError):
    """Raised for APIs that cannot work now the server runs inside Stash."""


def unsupported(what: str, instead: str) -> UnsupportedInProcess:
    return UnsupportedInProcess(
        f"{what} is not available now that the AI server runs inside Stash. "
        f"Use {instead} instead."
    )
