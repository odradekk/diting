"""Append-only JSONL log for routing decisions.

Each search query produces one or more :class:`RoutingDecision` entries
(one per round).  The log is designed for post-hoc auditing and for the
Phase 6 ``explain-last-query`` command.
"""

from __future__ import annotations

import json
import pathlib
import time
from dataclasses import asdict, dataclass, field

from diting.log import get_logger

logger = get_logger("routing.decision_log")

_DEFAULT_LOG_DIR = pathlib.Path.home() / ".cache" / "diting"
_DEFAULT_LOG_FILE = "routing_decisions.jsonl"


@dataclass
class RoutingDecision:
    """One routing decision for a single round of a query."""

    query_id: str
    round: int
    query: str
    routing_source: str  # "llm", "embedding", "evaluator", "all"
    included_modules: list[str]
    excluded_modules: list[str]
    cost_gated: list[str] = field(default_factory=list)
    reasons: dict[str, str] = field(default_factory=dict)
    timestamp: float = field(default_factory=time.time)


class RoutingDecisionLog:
    """Append-only JSONL file for routing decisions.

    Parameters
    ----------
    log_path:
        Path to the JSONL file.  ``None`` uses the default
        ``~/.cache/diting/routing_decisions.jsonl``.  An empty string
        disables persistence (decisions are only logged, not written).
    max_file_mb:
        Rotate (truncate) the file when it exceeds this size in MB.
        Keeps the last half of the file to preserve recent decisions.
    """

    def __init__(
        self,
        log_path: str | pathlib.Path | None = None,
        *,
        max_file_mb: float = 10.0,
    ) -> None:
        if log_path == "":
            self._path: pathlib.Path | None = None
        elif log_path is None:
            self._path = _DEFAULT_LOG_DIR / _DEFAULT_LOG_FILE
        else:
            self._path = pathlib.Path(log_path)
        self._max_bytes = int(max_file_mb * 1024 * 1024)

    @property
    def path(self) -> pathlib.Path | None:
        return self._path

    def record(self, decision: RoutingDecision) -> None:
        """Append a decision to the log file."""
        line = json.dumps(asdict(decision), ensure_ascii=False)

        logger.debug(
            "Routing decision: query_id=%s round=%d source=%s included=%s",
            decision.query_id,
            decision.round,
            decision.routing_source,
            decision.included_modules,
        )

        if self._path is None:
            return

        try:
            self._path.parent.mkdir(parents=True, exist_ok=True)
            with self._path.open("a", encoding="utf-8") as f:
                f.write(line + "\n")
            self._maybe_rotate()
        except OSError:
            logger.warning("Failed to write routing decision log", exc_info=True)

    def last_query(self, query_id: str | None = None) -> list[RoutingDecision]:
        """Read decisions for a query.

        When *query_id* is ``None``, returns the most recent query's
        decisions (all rounds).
        """
        if self._path is None or not self._path.is_file():
            return []

        try:
            lines = self._path.read_text(encoding="utf-8").strip().splitlines()
        except OSError:
            return []

        if not lines:
            return []

        # Parse all entries (newest last).
        entries: list[dict] = []
        for raw in reversed(lines):
            try:
                entries.append(json.loads(raw))
            except json.JSONDecodeError:
                continue

        if not entries:
            return []

        if query_id is None:
            query_id = entries[0]["query_id"]

        results = [
            RoutingDecision(**e)
            for e in entries
            if e.get("query_id") == query_id
        ]
        results.sort(key=lambda d: d.round)
        return results

    def _maybe_rotate(self) -> None:
        """Truncate the file if it exceeds the size limit."""
        if self._path is None:
            return
        try:
            size = self._path.stat().st_size
            if size <= self._max_bytes:
                return
            # Keep the last half.
            data = self._path.read_bytes()
            mid = len(data) // 2
            # Find the next newline after midpoint to avoid splitting a line.
            newline_pos = data.index(b"\n", mid)
            self._path.write_bytes(data[newline_pos + 1 :])
            logger.info("Rotated routing decision log: %d → %d bytes", size, len(data) - newline_pos - 1)
        except (OSError, ValueError):
            pass
