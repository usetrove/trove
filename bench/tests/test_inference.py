import itertools
import math

import pytest
from helpers import paired_runs

from trovebench.inference import (ParityUnit, cluster_bootstrap, holm, infer, mcnemar_exact, parity,
                                  sign_flip_permutation_test, wilcoxon_signed_rank)
from trovebench.stats import make_pairs

WILCOXON_EXAMPLE = [6, 8, 14, 16, 23, 24, 28, 29, 41, -48, 49, 56, 60, 67, 75]


def brute_wilcoxon_p(diffs):
    d = [x for x in diffs if x != 0]
    ranks = {i: r + 1 for r, i in enumerate(sorted(range(len(d)), key=lambda i: abs(d[i])))}
    w = sum(ranks[i] for i, x in enumerate(d) if x > 0)
    sums = [sum(r for r, s in zip(ranks.values(), signs) if s) for signs in itertools.product([0, 1], repeat=len(d))]
    lo = sum(s <= w for s in sums) / len(sums)
    hi = sum(s >= w for s in sums) / len(sums)
    return min(1.0, 2 * min(lo, hi))


def test_wilcoxon_matches_brute_force():
    for diffs in ([1, -2, 3, 4, -5], [0.5, 1.5, -2.5, 3.5], WILCOXON_EXAMPLE[:9] + [-3]):
        assert wilcoxon_signed_rank(diffs)[1] == pytest.approx(brute_wilcoxon_p(diffs))


def test_wilcoxon_known_example():
    w, p = wilcoxon_signed_rank(WILCOXON_EXAMPLE)
    assert w == 110 and p == pytest.approx(0.00262451171875)  # value from scipy.stats.wilcoxon, 1.17.1


def test_wilcoxon_edge_cases():
    assert wilcoxon_signed_rank([]) == (0.0, 1.0)
    assert wilcoxon_signed_rank([0, 0, 0]) == (0.0, 1.0)
    assert wilcoxon_signed_rank([-1, -2, -3, -4, -5, -6])[1] == pytest.approx(2 / 64)


def test_wilcoxon_agrees_with_scipy():
    stats = pytest.importorskip("scipy.stats")
    exact = [0.31, -0.12, 0.44, 0.9, -0.06, 0.27, 0.61, 0.18, -0.33, 0.72, 0.05, 0.39]
    assert wilcoxon_signed_rank(exact)[1] == pytest.approx(stats.wilcoxon(exact).pvalue)
    ties = [1, 1, 2, -2, 3, 3, 4, 5, -5, 6, 7, 7, 8, 9, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24]
    # SciPy's default for ties is the normal approximation without continuity correction; compare with it.
    assert wilcoxon_signed_rank(ties)[1] == pytest.approx(stats.wilcoxon(ties, correction=True, method="approx").pvalue, rel=1e-6)


def test_permutation_exact_and_monte_carlo():
    assert sign_flip_permutation_test([1, 1, 1, 1], resamples=100, seed=1) == pytest.approx(2 / 16)
    assert sign_flip_permutation_test([1, -1], resamples=100, seed=1) == 1.0
    assert sign_flip_permutation_test([], resamples=10, seed=1) == 1.0
    d = [1.0] * 30
    p = sign_flip_permutation_test(d, resamples=2000, seed=3)
    assert p < 0.01 and p == sign_flip_permutation_test(d, resamples=2000, seed=3)


def test_mcnemar_known_values():
    assert mcnemar_exact(2, 10) == pytest.approx(79 * 2 / 4096)
    assert mcnemar_exact(0, 0) == 1.0
    assert mcnemar_exact(5, 5) == 1.0


def test_mcnemar_agrees_with_scipy_binomtest():
    stats = pytest.importorskip("scipy.stats")
    assert mcnemar_exact(3, 11) == pytest.approx(stats.binomtest(3, 14, 0.5).pvalue)


def test_holm():
    assert holm([0.01, 0.04, 0.03]) == pytest.approx([0.03, 0.06, 0.06])
    assert holm([0.5]) == [0.5]
    assert holm([0.4, 0.9]) == pytest.approx([0.8, 0.9])
    assert all(a <= 1 for a in holm([0.6, 0.7, 0.8]))


def test_cluster_bootstrap_resamples_whole_clusters():
    clusters = [[10, 10], [0, 0]]
    lo, hi = cluster_bootstrap(clusters, lambda xs: sum(xs) / len(xs), resamples=500, seed=1, confidence=0.95)
    # only whole clusters can be drawn, so the mean can be 0, 5 or 10 and never anything in between
    assert (lo, hi) == (0.0, 10.0)
    with pytest.raises(ValueError):
        cluster_bootstrap([], sum, resamples=10, seed=1, confidence=0.9)


def test_infer_detects_a_real_saving_and_no_effect():
    recs = paired_runs(80, 1.0, lambda i: 0.8 + 0.01 * (i % 5), ref="direct", treated="hooks")
    inf = infer(make_pairs(recs, "direct", "hooks"), resamples=1000, seed=1, confidence=0.95)
    assert inf.n_pairs == 80 and inf.n_tasks == 40
    assert inf.median_cost_change_pct == pytest.approx(-18, abs=1) and inf.median_cost_change_ci[1] < -15
    assert inf.p_wilcoxon < 1e-6 and inf.p_permutation < 0.01
    assert inf.p_mcnemar == 1.0 and inf.success_diff_pts == 0

    null = paired_runs(40, lambda i: 1.0 + 0.1 * (i % 2), lambda i: 1.0 + 0.1 * ((i + 1) % 2), ref="direct", treated="hooks")
    inf = infer(make_pairs(null, "direct", "hooks"), resamples=1000, seed=1, confidence=0.95)
    assert inf.median_cost_change_ci[0] < 0 < inf.median_cost_change_ci[1] or inf.median_cost_diff_usd == 0
    assert infer([], resamples=100, seed=1, confidence=0.95) is None


def test_infer_success_loss_is_flagged_by_mcnemar():
    recs = paired_runs(60, 1.0, 1.0, tr_pass=lambda i: i % 3 != 0, ref="direct", treated="hooks")
    inf = infer(make_pairs(recs, "direct", "hooks"), resamples=1000, seed=1, confidence=0.95)
    assert inf.only_reference_passed == 20 and inf.only_treated_passed == 0
    assert inf.p_mcnemar < 1e-4 and inf.success_diff_pts == pytest.approx(-100 / 3)


def test_parity_ratio_and_ci():
    units = [ParityUnit(f"t{i}", 1.0, 0.85 + 0.01 * (i % 3), 0.70 + 0.01 * (i % 3)) for i in range(40)]
    p = parity(units, resamples=1000, seed=1, confidence=0.95)
    assert p.estimable
    assert p.gateway_saving_pct == pytest.approx(29, abs=0.5) and p.plugin_saving_pct == pytest.approx(14, abs=0.5)
    assert p.plugin_share_of_gateway_pct == pytest.approx(14 / 29 * 100, abs=1.5)
    lo, hi = p.plugin_share_ci
    assert lo < p.plugin_share_of_gateway_pct < hi


def test_parity_not_estimable_when_gateway_saves_nothing():
    units = [ParityUnit(f"t{i}", 1.0, 0.9, 1.0 + (0.05 if i % 2 else -0.05)) for i in range(30)]
    p = parity(units, resamples=1000, seed=1, confidence=0.95)
    assert not p.estimable and p.plugin_share_of_gateway_pct is None and "Not estimable" in p.note
    assert not math.isnan(p.plugin_saving_pct)
    with pytest.raises(ValueError):
        parity([], resamples=100, seed=1, confidence=0.95)
