# CanaryLens

CanaryLens is a Kubernetes canary rollout controller with a local demo mode. It gradually increases traffic to a new release, checks Prometheus every 15 seconds, and rolls back when the canary's 5xx rate exceeds 1% over the rolling minute. A small Python gRPC service scores the canary against the stable release and returns a human-readable decision note. The React dashboard shows rollout state and its decision history.

## Components

- `controller/`: Go HTTP API, rollout engine, Prometheus queries, PostgreSQL event history, gRPC scoring client, and Kubernetes client setup.
- `scorer/`: Python gRPC scoring service.
- `dashboard/`: React + TypeScript dashboard.
- `deploy/`: CRD, RBAC, and controller deployment manifests.
- `compose.yaml`: one-command local demo stack.

## Run the local demo

Requirements: Docker Compose.

```sh
docker compose up --build
```

Open <http://localhost:3000>. Choose **Start bad release** to observe a rollback after the first failing check, or **Start healthy release** to watch the rollout progress. The demo uses a deterministic metric simulator so the behavior is repeatable; it does not require a Kubernetes cluster or cloud credentials. The API is available at <http://localhost:8080/api/rollouts>.

## Run against Kubernetes

Set `MODE=kubernetes` to enable the controller's in-cluster reconciler. It reads `CanaryRollout` custom resources in the configured namespace and reconciles their status and NGINX Ingress canary weight. Prometheus must be reachable at `PROMETHEUS_URL`; the scorer at `SCORER_ADDR`; PostgreSQL at `DATABASE_URL`.

1. Create the API and database credentials as Kubernetes Secrets. Use a token with at least 32 characters. Store a PostgreSQL URL with `sslmode=require` when supported by your database:

   ```sh
   kubectl -n default create secret generic canarylens-api --from-literal=token='<random-32+-character-token>'
   kubectl -n default create secret generic canarylens-db --from-literal=url='postgres://USER:PASSWORD@HOST:5432/canarylens?sslmode=require'
   kubectl create secret tls canarylens-tls --cert=fullchain.pem --key=privkey.pem
   ```

2. Install the CRD and RBAC:

   ```sh
   kubectl apply -f deploy/crd.yaml
   kubectl apply -f deploy/rbac.yaml
   ```

3. Apply `deploy/scorer.yaml`, `deploy/controller.yaml`, and `deploy/dashboard.yaml`, replacing `YOUR_ACCOUNT`, Prometheus address, and `canarylens.example.com` with your values. The ingress serves the dashboard and routes `/api` to the bearer-token protected controller over TLS.
4. Apply an example rollout after creating stable/canary Services and the corresponding stable and canary NGINX Ingresses:

   ```sh
   kubectl apply -f deploy/example-rollout.yaml
   kubectl apply -f deploy/ingress.yaml
   ```

Create stable and canary Services with live endpoints before starting a rollout. CanaryLens waits for both services to have ready endpoints and for stable request metrics before sending the initial traffic step, then waits for canary metrics before advancing further. Set `spec.ingressName`, `spec.stableService`, and `spec.canaryService` to resources in the same namespace. `spec.ingressName` must identify the **canary Ingress**: it should route the same host/path as your stable Ingress and point to the canary Service. The controller writes the ingress-nginx canary annotations on that canary Ingress; the stable Ingress remains the baseline route. Prometheus must expose `http_requests_total` with `service` and `status` labels plus `http_request_duration_seconds_bucket` with the `service` label. The manifests use placeholder images and hostnames; replace them for your cluster. The gRPC scorer connection is plaintext inside the cluster network; use a service mesh or add gRPC TLS before crossing a trusted network boundary.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Controller API port |
| `DATABASE_URL` | unset for direct demo runs; Compose supplies local PostgreSQL | PostgreSQL connection; required in Kubernetes mode |
| `SCORER_ADDR` | `localhost:50051` | Python gRPC scorer |
| `PROMETHEUS_URL` | `http://localhost:9090` | Prometheus HTTP API |
| `NAMESPACE` | `default` | Namespace watched in Kubernetes mode |
| `API_TOKEN` | unset in local demo; required in Kubernetes mode | Bearer token protecting controller HTTP endpoints |
| `CORS_ORIGINS` | `http://localhost:3000` | Comma-separated browser origins allowed to call the API |
| `CHECK_INTERVAL` | `15s` | Time between metric checks |
| `ERROR_THRESHOLD` | `0.01` | Maximum canary 5xx ratio |
| `ROLLOUT_STEPS` | `5,25,50,100` | Canary traffic weights |

## Rollout behavior

In demo mode, the rollout progresses through 5%, 25%, 50%, and 100% traffic steps. Each check evaluates the rolling 1-minute canary 5xx ratio. Above 1%, the controller immediately sets canary traffic to zero and records a rollback event. In Kubernetes mode, the same decision logic is driven by Prometheus's `http_requests_total` series labeled with `service` and `status`.

The Python scorer compares stable and canary error rates and latency, then returns a score plus a concise explanation. The score is advisory; the hard error-rate threshold controls rollback. The dashboard can read the Kubernetes controller API through a browser-visible endpoint; use a port-forward or ingress for local cluster access.

## Checks and evidence

Run controller unit tests from `controller/` with `go test ./...`. Run scorer and manifest checks from the project root with `python -m pip install -r scorer/requirements.txt -r requirements-dev.txt`, then `python -m unittest discover -s scorer -v` and `python scripts/validate_yaml.py`. Build the dashboard with `cd dashboard && npm ci && npm run build`. GitHub Actions runs those checks and builds all three images before publishing images from pushes.

The Go test covers 25 synthetic bad-release decisions. `scripts/smoke_demo.py` exercises 25 full HTTP demo rollouts and measures the recorded metric-sample-to-rollback delay; CI runs it against the Compose stack, including PostgreSQL and the real gRPC scorer. A local in-memory run measured 1.7 ms median and 37.2 ms maximum. Those numbers describe the synthetic demo decision path, not a Kubernetes cluster, real traffic, or end-user rollback time. No 9-second cluster median is claimed until measured against a running cluster with the target ingress and metrics setup.

## Development

Go source is in `controller/`, scorer source in `scorer/`, and the Vite app in `dashboard/`. The Compose stack persists audit events in PostgreSQL; when running the demo controller directly without `DATABASE_URL`, it uses an in-memory event buffer for development only. The local Compose database credentials are demo-only. The cluster manifests read database and bearer credentials from Secrets and terminate external TLS at ingress. The controller has no workload image-promotion or Deployment patching logic; it adjusts traffic for already-created stable/canary Services. Configure backups, secret rotation, network policy, internal gRPC TLS, and identity integration before production use.

