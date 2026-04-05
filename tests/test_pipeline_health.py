"""Tests for diting.pipeline.health — passive engine health tracker."""

from diting.pipeline.health import HealthTracker


class _FakeClock:
    """Manually advanced monotonic clock for deterministic trip timing."""

    def __init__(self, start: float = 1000.0) -> None:
        self.now = start

    def __call__(self) -> float:
        return self.now

    def advance(self, seconds: float) -> None:
        self.now += seconds


# ---------------------------------------------------------------------------
# should_call / record_success / record_failure basic behaviour
# ---------------------------------------------------------------------------


def test_fresh_module_is_callable() -> None:
    tracker = HealthTracker()
    assert tracker.should_call("bing") is True


def test_success_keeps_module_callable() -> None:
    tracker = HealthTracker()
    for _ in range(10):
        tracker.record_success("bing")
    assert tracker.should_call("bing") is True
    assert tracker.get_weight("bing") == 1.0


def test_single_failure_does_not_trip() -> None:
    tracker = HealthTracker()
    tracker.record_failure("bing")
    assert tracker.should_call("bing") is True


def test_two_failures_do_not_trip() -> None:
    tracker = HealthTracker()
    tracker.record_failure("bing")
    tracker.record_failure("bing")
    assert tracker.should_call("bing") is True


# ---------------------------------------------------------------------------
# Short trip (3 consecutive failures → 5 min)
# ---------------------------------------------------------------------------


def test_three_failures_trigger_short_trip() -> None:
    clock = _FakeClock()
    tracker = HealthTracker(clock=clock)
    for _ in range(3):
        tracker.record_failure("bing")
    assert tracker.should_call("bing") is False


def test_short_trip_expires_after_five_minutes() -> None:
    clock = _FakeClock()
    tracker = HealthTracker(clock=clock)
    for _ in range(3):
        tracker.record_failure("bing")

    clock.advance(5 * 60 - 1)
    assert tracker.should_call("bing") is False

    clock.advance(2)  # now past the 5-minute window
    assert tracker.should_call("bing") is True


# ---------------------------------------------------------------------------
# Long trip (5 consecutive failures → 30 min)
# ---------------------------------------------------------------------------


def test_five_failures_trigger_long_trip() -> None:
    clock = _FakeClock()
    tracker = HealthTracker(clock=clock)
    for _ in range(5):
        tracker.record_failure("bing")

    # Short trip window has passed, but long trip still holds.
    clock.advance(10 * 60)
    assert tracker.should_call("bing") is False

    # Past 30 minutes total → callable again.
    clock.advance(21 * 60)
    assert tracker.should_call("bing") is True


# ---------------------------------------------------------------------------
# Success resets the circuit breaker
# ---------------------------------------------------------------------------


def test_success_resets_consecutive_failures() -> None:
    tracker = HealthTracker()
    tracker.record_failure("bing")
    tracker.record_failure("bing")
    tracker.record_success("bing")
    tracker.record_failure("bing")
    tracker.record_failure("bing")
    # Only 2 consecutive failures since the success — not tripped.
    assert tracker.should_call("bing") is True


def test_success_clears_active_trip() -> None:
    tracker = HealthTracker()
    for _ in range(3):
        tracker.record_failure("bing")
    assert tracker.should_call("bing") is False

    tracker.record_success("bing")
    assert tracker.should_call("bing") is True


# ---------------------------------------------------------------------------
# Failure immediately after trip expiry can re-trip
# ---------------------------------------------------------------------------


def test_failures_survive_trip_expiry_and_can_escalate() -> None:
    clock = _FakeClock()
    tracker = HealthTracker(clock=clock)
    for _ in range(3):
        tracker.record_failure("bing")

    # Wait out the short trip.
    clock.advance(5 * 60 + 1)
    assert tracker.should_call("bing") is True

    # Two more failures after expiry → hits 5 consecutive → long trip.
    tracker.record_failure("bing")
    tracker.record_failure("bing")
    assert tracker.should_call("bing") is False
    clock.advance(10 * 60)
    assert tracker.should_call("bing") is False  # still in long trip


# ---------------------------------------------------------------------------
# Sliding window deweighting
# ---------------------------------------------------------------------------


def test_weight_is_one_for_small_window() -> None:
    tracker = HealthTracker()
    # Fewer than 20 samples — no deweighting yet.
    tracker.record_failure("bing")
    tracker.record_failure("bing")
    assert tracker.get_weight("bing") == 1.0


def test_weight_deweights_when_window_mostly_fails() -> None:
    tracker = HealthTracker()
    # 18 failures + 2 successes = 10% success rate, below 30% threshold.
    for _ in range(18):
        tracker.record_failure("bing")
    tracker.record_success("bing")
    tracker.record_success("bing")
    assert tracker.get_weight("bing") == 0.5


def test_weight_stays_one_when_window_mostly_succeeds() -> None:
    tracker = HealthTracker()
    for _ in range(20):
        tracker.record_success("bing")
    assert tracker.get_weight("bing") == 1.0


def test_weight_not_deweighted_at_threshold_exactly() -> None:
    # 6 failures, 14 successes → 70% success rate, above 30% threshold.
    tracker = HealthTracker()
    for _ in range(6):
        tracker.record_failure("bing")
    for _ in range(14):
        tracker.record_success("bing")
    assert tracker.get_weight("bing") == 1.0


# ---------------------------------------------------------------------------
# Per-module isolation
# ---------------------------------------------------------------------------


def test_modules_tracked_independently() -> None:
    tracker = HealthTracker()
    for _ in range(3):
        tracker.record_failure("bing")
    assert tracker.should_call("bing") is False
    assert tracker.should_call("brave") is True


# ---------------------------------------------------------------------------
# Snapshot
# ---------------------------------------------------------------------------


def test_snapshot_reports_tracked_modules() -> None:
    tracker = HealthTracker()
    tracker.record_success("bing")
    tracker.record_failure("brave")
    snap = tracker.snapshot()

    assert set(snap.keys()) == {"bing", "brave"}
    assert snap["bing"]["consecutive_failures"] == 0
    assert snap["brave"]["consecutive_failures"] == 1
    assert snap["bing"]["tripped"] is False
    assert snap["brave"]["tripped"] is False


def test_snapshot_reflects_active_trip() -> None:
    clock = _FakeClock()
    tracker = HealthTracker(clock=clock)
    for _ in range(3):
        tracker.record_failure("bing")
    snap = tracker.snapshot()

    assert snap["bing"]["tripped"] is True
    assert snap["bing"]["trip_remaining_s"] == 5 * 60
