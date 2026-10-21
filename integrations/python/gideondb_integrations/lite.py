"""Run a local ``gideondb -lite`` process for notebooks, tests and scripts.

GideonDB is a Go program, so Python gets "embedded" mode by running the
single binary as a child process on a free loopback port. It needs no API key
or TLS and stops when the context exits::

    with LiteServer("./data") as db:      # db is a gideondb.Client
        db.create_collection({...})
"""

from __future__ import annotations

import os
import shutil
import socket
import subprocess
import time
import urllib.request
from typing import Any


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def find_binary(binary: str | None = None) -> str:
    """Resolve the gideondb binary: argument, ``GIDEONDB_BINARY``, then ``PATH``."""
    candidate = binary or os.environ.get("GIDEONDB_BINARY") or shutil.which("gideondb")
    if not candidate:
        raise FileNotFoundError(
            "gideondb binary not found; install it (go install github.com/Bharanipbk/gideondb/cmd/gideondb@latest) "
            "or set GIDEONDB_BINARY"
        )
    return candidate


class LiteServer:
    """Start ``gideondb -lite`` on loopback and yield a REST client for it."""

    def __init__(
        self,
        data_path: str,
        *,
        binary: str | None = None,
        port: int | None = None,
        startup_timeout: float = 30.0,
        extra_args: list[str] | None = None,
    ) -> None:
        if not data_path:
            raise ValueError("data_path is required")
        self.data_path = os.path.abspath(data_path)
        self.binary = binary
        self.port = port
        self.startup_timeout = startup_timeout
        self.extra_args = list(extra_args or [])
        self.process: subprocess.Popen[bytes] | None = None

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def command(self) -> list[str]:
        return [find_binary(self.binary), "-lite", "-data-path", self.data_path, "-http-address", f"127.0.0.1:{self.port}", *self.extra_args]

    def start(self) -> str:
        if self.process is not None:
            return self.url
        if self.port is None:
            self.port = _free_port()
        environment = {k: v for k, v in os.environ.items() if k not in ("GIDEONDB_API_KEY", "GIDEONDB_DATA_PATH", "GIDEONDB_HTTP_ADDRESS")}
        self.process = subprocess.Popen(self.command(), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, env=environment)
        deadline = time.monotonic() + self.startup_timeout
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                error = self.process.stderr.read().decode(errors="replace") if self.process.stderr else ""
                self.process = None
                raise RuntimeError(f"gideondb -lite exited during startup: {error.strip()}")
            try:
                with urllib.request.urlopen(f"{self.url}/v1/ready", timeout=1) as response:
                    if response.status == 200:
                        return self.url
            except OSError:
                time.sleep(0.1)
        self.stop()
        raise TimeoutError(f"gideondb -lite was not ready within {self.startup_timeout} seconds")

    def stop(self) -> None:
        if self.process is None:
            return
        self.process.terminate()
        try:
            self.process.wait(timeout=30)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
        if self.process.stderr:
            self.process.stderr.close()
        self.process = None

    def client(self) -> Any:
        from gideondb import Client

        return Client(self.start())

    def __enter__(self) -> Any:
        return self.client()

    def __exit__(self, *exc: object) -> None:
        self.stop()
