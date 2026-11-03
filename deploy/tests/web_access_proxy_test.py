import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('proxy_upgrade', ROOT / 'deploy/update-web-access-proxy.py')
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)


class ProxyUpgradeTest(unittest.TestCase):
    def test_preserves_bot_and_custom_routes(self):
        current = (ROOT / 'deploy/caddy/Caddyfile').read_text()
        original = current.replace(upgrade.NEW_PROXY, upgrade.OLD_PROXY, 1).replace(upgrade.NEW_REDIRECT, upgrade.OLD_REDIRECT)
        custom = '    # BEGIN WGBOT WEB CONNECT\n    handle /connect/* {\n        reverse_proxy 127.0.0.1:8001\n    }\n'
        original = original.replace('    # ── Decoy site', custom + upgrade.OLD_LINK + '    # ── Decoy site')
        updated = upgrade.upgrade(original)
        self.assertIn(custom, updated)
        self.assertNotIn('handle /cnf/', updated)
        self.assertIn(upgrade.NEW_PROXY, updated)
        self.assertEqual(upgrade.upgrade(updated), updated)

    def test_adds_metrics_to_previous_proxy_and_is_idempotent(self):
        current = (ROOT / 'deploy/caddy/Caddyfile').read_text()
        previous = current.replace(upgrade.METRICS_ROUTE, '').replace(upgrade.NEW_PROXY, upgrade.PREVIOUS_PROXY)
        updated = upgrade.upgrade(previous)
        self.assertEqual(updated, current)
        self.assertEqual(upgrade.upgrade(updated), updated)

    def test_refuses_custom_metrics_route(self):
        previous = (ROOT / 'deploy/caddy/Caddyfile').read_text().replace(upgrade.METRICS_ROUTE, '')
        previous = previous.replace('    # ── Admin interface', '    handle    /metrics { respond 200 }\n    # ── Admin interface')
        with self.assertRaises(ValueError):
            upgrade.upgrade(previous)

    def test_custom_proxy_fails_without_modification(self):
        with self.assertRaises(ValueError):
            upgrade.upgrade('handle_path /admin/* {\n reverse_proxy custom:9999\n}\n')


if __name__ == '__main__':
    unittest.main()
