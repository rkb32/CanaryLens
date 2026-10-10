import json
import unittest

import grpc
from server import create_server, score


class FakeContext:
    def abort(self, code, details):
        raise ValueError(f"{code}: {details}")


class ScorerTests(unittest.TestCase):
    def test_comparable_releases_receive_high_score(self):
        result = json.loads(score(json.dumps({
            "stable": {"error_rate": 0.002, "latency_ms": 115},
            "canary": {"error_rate": 0.002, "latency_ms": 130},
        }).encode(), FakeContext()))
        self.assertGreaterEqual(result["score"], 0.9)
        self.assertIn("close", result["reason"])

    def test_error_regression_is_explained(self):
        result = json.loads(score(json.dumps({
            "stable": {"error_rate": 0.002, "latency_ms": 115},
            "canary": {"error_rate": 0.035, "latency_ms": 130},
        }).encode(), FakeContext()))
        self.assertEqual(result["score"], 0.0)
        self.assertIn("errors are elevated", result["reason"])

    def test_latency_regression_is_explained(self):
        result = json.loads(score(json.dumps({
            "stable": {"error_rate": 0.002, "latency_ms": 100},
            "canary": {"error_rate": 0.002, "latency_ms": 140},
        }).encode(), FakeContext()))
        self.assertIn("latency regressed", result["reason"])

    def test_invalid_payload_returns_invalid_argument(self):
        with self.assertRaisesRegex(ValueError, "INVALID_ARGUMENT"):
            score(b"{}", FakeContext())

    def test_valid_payload_still_returns_score(self):
        result = json.loads(score(json.dumps({
            "stable": {"error_rate": 0.002, "latency_ms": 115},
            "canary": {"error_rate": 0.002, "latency_ms": 130},
        }).encode(), FakeContext()))
        self.assertIn("score", result)
        self.assertGreaterEqual(result["score"], 0.0)
        self.assertLessEqual(result["score"], 1.0)

    def test_nan_error_rate_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "INVALID_ARGUMENT"):
            score(json.dumps({
                "stable": {"error_rate": 0.002, "latency_ms": 115},
                "canary": {"error_rate": float("nan"), "latency_ms": 130},
            }).encode(), FakeContext())

    def test_infinite_latency_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "INVALID_ARGUMENT"):
            score(json.dumps({
                "stable": {"error_rate": 0.002, "latency_ms": 115},
                "canary": {"error_rate": 0.002, "latency_ms": float("inf")},
            }).encode(), FakeContext())

    def test_grpc_json_transport_round_trip(self):
        server = create_server()
        port = server.add_insecure_port("127.0.0.1:0")
        server.start()
        channel = grpc.insecure_channel(f"127.0.0.1:{port}")
        try:
            grpc.channel_ready_future(channel).result(timeout=5)
            call = channel.unary_unary(
                "/canarylens.Scorer/Score",
                request_serializer=lambda value: value,
                response_deserializer=lambda value: value,
            )
            response = call(json.dumps({
                "stable": {"error_rate": 0.002, "latency_ms": 115},
                "canary": {"error_rate": 0.035, "latency_ms": 130},
            }).encode(), timeout=5)
            self.assertIn("errors are elevated", json.loads(response)["reason"])
        finally:
            channel.close()
            server.stop(0).wait(timeout=5)


if __name__ == "__main__":
    unittest.main()
