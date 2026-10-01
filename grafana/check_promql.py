#!/usr/bin/env python3
"""Evaluate the exported dashboard queries with a supplied Prometheus promtool."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path


def main():
    if len(sys.argv) != 2:
        raise SystemExit("Usage: python3 grafana/check_promql.py /path/to/promtool")
    dashboard = json.loads(Path(__file__).with_name("cascade-dashboard.json").read_text())
    panels = dashboard["spec"]["elements"]

    def expression(panel, query=0, hidden=False, instance=".*", interface=".*"):
        expr = panels[f"panel-{panel}"]["spec"]["data"]["spec"]["queries"][query]["spec"]["query"]["spec"]["expr"]
        for name, value in {
            "$excluded_interface_role": "s2s" if hidden else "__none__",
            "$instance": instance,
            "$interface": interface,
            "$peer": ".*",
            "$gateway": ".*",
            "$__range": "4m",
            "$__rate_interval": "1m",
        }.items():
            expr = expr.replace(name, value)
        return expr

    # Each row represents one server/interface pair. The shared wg10 name tests
    # that exclusions join on instance as well as interface.
    rows = [
        ("a", "wg10", "alice", "Alice", "client", "0 10 20 0 10", "0 20 40 0 20", 30),
        ("a", "wg11", "tunnel", "Tunnel", "s2s", "0 100 200 300 400", "0 200 400 600 800", 1200),
        ("b", "wg10", "other", "Other", "s2s", "0 5 10 15 20", "0 5 10 15 20", 40),
        ("b", "wg12", "bob", "Bob", "client", "100 101 102 103 104", "200 202 204 206 208", 12),
    ]
    # More than 10 higher-traffic S2S peers ensure Hide filters before topk,
    # rather than removing S2S after they have already displaced clients.
    for index in range(11):
        step = 1000 + index
        values = " ".join(str(step * n) for n in range(5))
        rows.append(("a", f"wg{20 + index}", f"s2s{index}", f"S2S{index}", "s2s", values, values, 8 * step))
    inputs = []
    for instance, interface, peer, name, role, rx, tx, _ in rows:
        labels = f'instance="{instance}",interface="{interface}",peer_id="{peer}",name="{name}"'
        for metric, values in [("received", rx), ("sent", tx)]:
            inputs.append({"series": f"cascade_peer_{metric}_bytes_total{{{labels}}}", "values": values})
        base = f'instance="{instance}",interface="{interface}"'
        inputs.extend([
            {"series": f'cascade_interface_role_info{{{base},role="{role}"}}', "values": "1 1 1 1 1"},
            {"series": f"cascade_interface_peers{{{base}}}", "values": "1 1 1 1 1"},
        ])

    cases = []
    for hidden, instance, interface in [(False, ".*", ".*"), (True, ".*", ".*"), (True, "a", ".*"), (True, ".*", "wg10")]:
        selected = [r for r in rows if (not hidden or r[4] != "s2s") and (instance == ".*" or r[0] == instance) and (interface == ".*" or r[1] == interface)]
        clients = [r for r in selected if r[4] == "client"]
        samples = [{"labels": f'{{instance="{r[0]}",interface="{r[1]}",peer_id="{r[2]}",name="{r[3]}"}}', "value": r[7]} for r in sorted(clients, key=lambda r: r[7], reverse=True)[:10]]
        cases.extend([
            {"expr": expression("43", hidden=hidden, instance=instance, interface=interface), "eval_time": "4m", "exp_samples": samples},
            {"expr": expression("4", hidden=hidden, instance=instance, interface=interface), "eval_time": "4m", "exp_samples": [{"labels": "{}", "value": len(selected)}]},
        ])

    # All queries must parse and evaluate, including panels outside the S2S scope.
    empty_queries = []
    for key, panel in panels.items():
        for i, _ in enumerate(panel["spec"].get("data", {}).get("spec", {}).get("queries", [])):
            empty_queries.append({"expr": expression(key.removeprefix("panel-"), i), "eval_time": "4m", "exp_samples": []})

    # Grafana query_result() wraps PromQL; validate its inner expressions too.
    for variable in dashboard["spec"]["variables"]:
        definition = variable["spec"].get("definition", "")
        if not definition.startswith("query_result("):
            continue
        expr = definition[len("query_result("):-1]
        for name, value in {"$excluded_interface_role": "s2s", "$instance": ".*", "$interface": ".*"}.items():
            expr = expr.replace(name, value)
        empty_queries.append({"expr": expr, "eval_time": "4m", "exp_samples": []})

    legacy = [item for item in inputs if "cascade_interface_role_info" not in item["series"]]
    insufficient = [
        {"series": 'cascade_peer_received_bytes_total{instance="a",interface="wg10",peer_id="new",name="New"}', "values": "_ _ _ _ 100"},
        {"series": 'cascade_peer_sent_bytes_total{instance="a",interface="wg10",peer_id="new",name="New"}', "values": "_ _ _ _ 200"},
    ]
    # Caps remain effective for repeated resets, a reset observed only at the
    # next scrape, and nonzero lifetime totals carried into the month.
    reset_tests = []
    for title, rx, tx, expected in [
        ("Repeated resets", "0 100 0 100 10", "0 200 0 200 20", 30),
        ("Reset to zero", "1000 1100 1200 1300 0", "2000 2200 2400 2600 0", 0),
        ("Reset between scrapes with new traffic", "1000 1100 1200 1300 10", "2000 2200 2400 2600 20", 30),
        ("No monthly growth with lifetime usage", "100 100 100 100 100", "200 200 200 200 200", 0),
    ]:
        labels = 'instance="a",interface="wg10",peer_id="alice",name="Alice"'
        series = [
            {"series": f"cascade_peer_received_bytes_total{{{labels}}}", "values": rx},
            {"series": f"cascade_peer_sent_bytes_total{{{labels}}}", "values": tx},
            {"series": 'cascade_interface_role_info{instance="a",interface="wg10",role="client"}', "values": "1 1 1 1 1"},
        ]
        reset_tests.append({"name": title, "interval": "1m", "input_series": series, "promql_expr_test": [
            {"expr": expression("43"), "eval_time": "4m", "exp_samples": [{"labels": "{" + labels + "}", "value": expected}]},
        ]})
    fixture = {"evaluation_interval": "1m", "fuzzy_compare": True, "tests": [
        {"name": "Monthly reset cap and client-only ranking", "interval": "1m", "input_series": inputs, "promql_expr_test": cases},
        {"name": "Query syntax and empty history", "interval": "1m", "promql_expr_test": empty_queries},
        {"name": "Client ranking requires role metadata", "interval": "1m", "input_series": legacy, "promql_expr_test": [{"expr": expression("43", hidden=True), "eval_time": "4m", "exp_samples": []}]},
        {"name": "One scrape cannot reconstruct monthly traffic", "interval": "1m", "input_series": insufficient, "promql_expr_test": [{"expr": expression("43"), "eval_time": "4m", "exp_samples": []}]},
    ] + reset_tests}
    with tempfile.TemporaryDirectory(prefix="cascade-promql-") as directory:
        test_file = Path(directory) / "dashboard-tests.json"
        test_file.write_text(json.dumps(fixture))
        subprocess.run([sys.argv[1], "test", "rules", str(test_file)], check=True)


if __name__ == "__main__":
    main()
