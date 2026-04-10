"""Routing strategy presets.

A *strategy* is a named configuration that controls how the orchestrator
selects search modules for each round.  Users pick one via the
``ROUTING_STRATEGY`` environment variable.
"""

from __future__ import annotations

from typing import Literal

RoutingStrategy = Literal["funnel", "cheap_first", "fire_all"]

ALL_STRATEGIES: tuple[RoutingStrategy, ...] = ("funnel", "cheap_first", "fire_all")
