"""Small JSON-over-gRPC scorer; the transport remains gRPC while the payload stays inspectable."""
import json
import math
from concurrent import futures

import grpc


def score(payload: bytes, context: grpc.ServicerContext) -> bytes:
    try:
        request = json.loads(payload)
        stable = request["stable"]
        canary = request["canary"]
        stable_err = float(stable.get("error_rate", 0))
        canary_err = float(canary.get("error_rate", 0))
        stable_latency = float(stable.get("latency_ms", 0))
        canary_latency = float(canary.get("latency_ms", 0))
        if not all(math.isfinite(v) for v in (stable_err, canary_err, stable_latency, canary_latency)):
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "invalid scoring request: error_rate and latency_ms must be finite numbers")
        penalty = max(0.0, canary_err - stable_err) * 700 + max(0.0, canary_latency - stable_latency) / 1000
        value = max(0.0, min(1.0, 1.0 - penalty))
        if canary_err > stable_err * 1.5 + 0.002:
            reason = f"Canary errors are elevated ({canary_err:.2%} vs {stable_err:.2%} stable)."
        elif canary_latency > stable_latency * 1.25:
            reason = f"Canary latency regressed ({canary_latency:.0f} ms vs {stable_latency:.0f} ms stable)."
        else:
            reason = "Canary error rate and latency are close to the stable release."
        return json.dumps({"score": round(value, 3), "reason": reason}).encode()
    except (KeyError, TypeError, ValueError, AttributeError, json.JSONDecodeError) as exc:
        context.abort(grpc.StatusCode.INVALID_ARGUMENT, f"invalid scoring request: {exc}")


def create_server() -> grpc.Server:
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    handler = grpc.method_handlers_generic_handler(
        "canarylens.Scorer",
        {"Score": grpc.unary_unary_rpc_method_handler(
            score,
            request_deserializer=lambda raw: raw,
            response_serializer=lambda raw: raw,
        )},
    )
    server.add_generic_rpc_handlers((handler,))
    return server


def main() -> None:
    server = create_server()
    server.add_insecure_port("[::]:50051")
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    main()
