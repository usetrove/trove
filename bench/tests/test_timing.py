"""Pilot time estimate: per-session time from the pilot, projected to one look and to the looks that fit."""

from __future__ import annotations

import pytest

from trovebench.timing import describe, estimate_time


def test_per_session_includes_setup_and_grading_overhead():
    # 4 sessions, agent time 100s each, but the pilot took 600s overall: 50s per session went to containers and grading.
    est = estimate_time([100, 100, 100, 100], elapsed_s=600, units_per_look=120, looks=2)
    assert est.per_session_s == pytest.approx(150)
    assert est.per_look_h == pytest.approx(150 * 120 / 3600)
    assert est.total_h == pytest.approx(2 * 150 * 120 / 3600)


def test_sizes_with_a_margin_for_the_slowest_session():
    # Mean agent time 100s, slowest 200s: size with 0.75 x 200 = 150s, as the cost sizing does.
    est = estimate_time([50, 50, 100, 200], elapsed_s=400, units_per_look=10, looks=1)
    assert est.per_session_s == pytest.approx(150)


def test_without_elapsed_time_uses_agent_time_only_and_says_so():
    est = estimate_time([60, 60], elapsed_s=None, units_per_look=10, looks=1)
    assert est.per_session_s == pytest.approx(60)
    assert not est.includes_overhead
    assert "agent time only" in describe(est)


def test_a_resumed_pilot_shorter_than_its_agent_time_is_treated_as_untimed():
    est = estimate_time([300, 300], elapsed_s=200, units_per_look=10, looks=1)
    assert est.per_session_s == pytest.approx(300)
    assert not est.includes_overhead


def test_no_timed_sessions_gives_no_estimate():
    assert estimate_time([], elapsed_s=500, units_per_look=10, looks=1) is None


def test_description_is_labelled_estimated_and_counts_six_hour_sittings():
    est = estimate_time([240] * 6, elapsed_s=6 * 300, units_per_look=120, looks=3)  # 5 min each
    text = describe(est)
    assert "estimated" in text
    assert "5.0 min per session" in text
    assert "10.0 h per look" in text
    assert "30.0 h for 3 look(s)" in text
    assert "5 sitting(s) of 6 h" in text
    assert "resume" in text
