"""Passive engine health tracker for search modules.

Tracks per-module call outcomes (success / failure) observed during ordinary
search traffic and enforces a simple circuit breaker:

- **3 consecutive failures** → module is tripped for 5 minutes
- **5 consecutive failures** → tripped for 30 minutes
- **Success rate < 30%** over the last 20 calls → deweighted to 0.5

The tracker is strictly **passive**: it never sends probe requests that could
draw anti-bot attention. State lives in memory only and clears on process
restart — that is intentional, so a stale circuit-breaker record can never
prevent calls after a restart.

Thresholds and durations are module-level constants, deliberately hardcoded
for now. Once real traffic informs good defaults they can graduate to config.
"""

from __future__ import annotations

import time
from collections import deque
from collections.abc import Callable
from dataclasses import dataclass, field

from diting.log import get_logger

logger = get_logger("pipeline.health")

# Circuit breaker thresholds ------------------------------------------------
_SHORT_TRIP_THRESHOLD = 3
_LONG_TRIP_THRESHOLD = 5
_SHORT_TRIP_DURATION_S = 5 * 60
_LONG_TRIP_DURATION_S = 30 * 60

# Sliding window deweighting ------------------------------------------------
_WINDOW_SIZE = 20
_DEWEIGHT_SUCCESS_RATE = 0.3
_DEWEIGHTED_VALUE = 0.5


@dataclass
class _ModuleHealth:
    """Internal per-module state — not part of the public API."""

    consecutive_failures: int = 0
    tripped_until: float = 0.0  # monotonic timestamp; 0.0 = not tripped
    window: deque[bool] = field(
        default_factory=lambda: deque(maxlen=_WINDOW_SIZE)
    )


class HealthTracker:
    """Tracks per-module success / failure and gates calls via circuit breaker.

    Typical usage from the orchestrator::

        if not health.should_call(module.name):
            continue  # skip — module is currently tripped
        output = await module.search(query)
        if output.error:
            health.record_failure(module.name)
        else:
            health.record_success(module.name)

    The tracker is cheap to construct and holds only in-memory state, so a
    single instance per :class:`Orchestrator` is sufficient.
    """

    def __init__(self, clock: Callable[[], float] | None = None) -> None:
        # ``clock`` is exposed for tests; production code uses the default.
        self._clock = clock or time.monotonic
        self._modules: dict[str, _ModuleHealth] = {}

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def should_call(self, module: str) -> bool:
        """Return ``False`` while ``module`` is inside a trip cooldown."""
        state = self._get(module)
        now = self._clock()
        if state.tripped_until and now < state.tripped_until:
            return False
        if state.tripped_until and now >= state.tripped_until:
            # Cooldown expired — clear the trip but keep the consecutive count
            # so that a single follow-up failure does not immediately re-trip.
            state.tripped_until = 0.0
        return True

    def record_success(self, module: str) -> None:
        """Reset consecutive failures and append a success to the window."""
        state = self._get(module)
        state.consecutive_failures = 0
        state.tripped_until = 0.0
        state.window.append(True)

    def record_failure(self, module: str) -> None:
        """Increment the failure counter and trip the breaker if needed."""
        state = self._get(module)
        state.consecutive_failures += 1
        state.window.append(False)
        self._maybe_trip(module, state)

    def get_weight(self, module: str) -> float:
        """Return 1.0 normally, 0.5 when the sliding window is degraded.

        Requires the window to be full (20 samples) before considering
        deweighting, so early ramp-up failures do not punish a module.
        """
        state = self._get(module)
        if len(state.window) < _WINDOW_SIZE:
            return 1.0
        success_rate = sum(1 for ok in state.window if ok) / len(state.window)
        if success_rate < _DEWEIGHT_SUCCESS_RATE:
            return _DEWEIGHTED_VALUE
        return 1.0

    def snapshot(self) -> dict[str, dict[str, object]]:
        """Return a logging-friendly view of every tracked module."""
        now = self._clock()
        result: dict[str, dict[str, object]] = {}
        for name, state in self._modules.items():
            success_rate: float | None = None
            if state.window:
                success_rate = sum(1 for ok in state.window if ok) / len(state.window)
            trip_remaining_s = (
                max(0.0, state.tripped_until - now) if state.tripped_until else 0.0
            )
            result[name] = {
                "consecutive_failures": state.consecutive_failures,
                "window_size": len(state.window),
                "success_rate": success_rate,
                "tripped": trip_remaining_s > 0,
                "trip_remaining_s": trip_remaining_s,
                "weight": self.get_weight(name),
            }
        return result

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _get(self, module: str) -> _ModuleHealth:
        state = self._modules.get(module)
        if state is None:
            state = _ModuleHealth()
            self._modules[module] = state
        return state

    def _maybe_trip(self, module: str, state: _ModuleHealth) -> None:
        if state.consecutive_failures >= _LONG_TRIP_THRESHOLD:
            duration = _LONG_TRIP_DURATION_S
            level = "long"
        elif state.consecutive_failures >= _SHORT_TRIP_THRESHOLD:
            duration = _SHORT_TRIP_DURATION_S
            level = "short"
        else:
            return
        state.tripped_until = self._clock() + duration
        logger.warning(
            "Circuit tripped (%s): module=%s consecutive_failures=%d duration_s=%d",
            level,
            module,
            state.consecutive_failures,
            duration,
        )
