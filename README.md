# Real time ui real time UI update microservice
A plug and play solution for real time updates on the UI for distributed systems.

## 🛠️ Technology Stack
|Component |	Technology | Purpose |
|----------|-----------|----------|
|Microservice Runtime|	<img height="40" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/go.png"> Go |High-concurrency WebSocket handling|
|Web Server|<img height="40" src="https://raw.githubusercontent.com/marwin1991/profile-technology-icons/refs/heads/main/icons/nginx.png"> Nginx |Reverse proxy and SSL termination|
|Authentication| TOTP (Time-based One-Time Password)	|Secure service-to-service communication|

## Overview

When developing a full stack system there always arises the need of real time updates on UIs, but when the system is distributed between front system and back system this real time update usually requires adding an overhead to the backend of memory usage, concurrency handling, code complexity, etc.
In this repo I provide a "Plug and play" Golang microservice that would mitigate this issues by providing a simple and optimized for true parallelism real time updates handler.

# 🔐 Security Model
- Shared secret authentication between services
- Time-based passwords prevent that if a used token gets compromised, after a configurable time window it will stop being valid
- HTTPS encryption for all service-to-service communication
- WebSocket connections secured with WSS protocol + JWT for user authentication.


## 💡 How It Works

This microservice is designed to address the challenges of adding real-time updates to distributed systems without increasing the complexity or resource consumption of the main backend.

When a significant event occurs on your main backend (e.g., a new object is created), the backend sends a simple POST request to the Go microservice. This request contains the payload intended for the frontend.

Upon receiving the request, the microservice performs a security check using a shared secret to validate a Time-Based Token. If the token is valid, it broadcasts the payload to all connected clients via their established WebSocket connections.

Thanks to Go's Goroutines, this broadcast happens in parallel. This ensures that a large number of clients can receive updates simultaneously without a single slow client blocking the entire process, leading to high throughput and low latency.

The distributed advantage
For global applications, you can deploy a microservice node in each major geographical region. Instead of your main backend broadcasting to clients worldwide, it only needs to send a single request to the local microservice node. This node then handles the local broadcast, significantly reducing inter-continental bandwidth usage and improving update speed for regional clients.

<img width="100%" src="https://github.com/SantiagoLopezDeharo/real-time-ui-update-microservice/blob/main/arquitecture.png?raw=true">

# 🎯 Use Cases & Advantages

## Simpler, more focused backends
Modern backend systems can quickly become complex. By offloading real-time update logic to a dedicated microservice, you can keep your main backend clean and focused on business logic. This separation of concerns simplifies development, bug fixing, and future feature additions.

## Overcoming framework limitations
Frameworks like Node.js, while excellent for I/O-bound tasks, struggle with CPU-intensive operations and true parallelism due to their single-threaded nature. Broadcasting to a large number of clients can be slow and memory-intensive while also handling multuple WS connection can generate too much pressure on the main event loop, while solutions with work threads are possible they are far from ideal and introduce too much complexity to the system without achieving reliable results.

Go's concurrency model, with its lightweight Goroutines and built-in scheduler, excels at handling many concurrent connections efficiently. This leads to a smaller memory footprint and faster broadcast times, making it an ideal choice for this type of service.

By combining the to with an easy web hook integration we get fast and reliable development for CRUD operations logic using NodeJS for the business logic layer while not letting NodeJS runtime limitations slow down other features.

## Bandwidth and resource optimization
Deploying microservice nodes closer to your users reduces the burden on your main server. It minimizes the amount of data sent across long distances, lowering bandwidth costs and freeing up resources on your central infrastructure. This approach makes your overall system more reliable and cost-effective.

# 🚀 Demo

Prerequisites
- Go (version 1.27.3 or higher)
- Node.js & npm (for demo tools)

Example .env file:
```shell
JWT_SECRET=your-jwt-secret
TIME_TOKEN_SECRET=your-time-token-secret
PORT=8080
TIME_WINDOW_SECONDS=30
ALLOWED_CLOCK_SKEW=1
```

Example demo-tools/backend-simulator/.env
```shell
API_URL=http://localhost:8080/update
TIME_TOKEN_SECRET=your-time-token-secret
TIME_WINDOW_SECONDS=30
```

Example demo-tools/frontend-cli/.env
```shell
WS_URL=ws://localhost:8080/ws
JWT_SECRET=your-jwt-secret
USER_ID=demo-user-123
```

Setting up microservice:
```shell
go run ./cmd/server
```

Running demo frontend (node js front)
```shell
cd demo-tools/frontend-cli && npm i && npm start 
```

Running demo simulated backend (node js back)
```shell
cd demo-tools/backend-simulator && npm i && npm start 
```

Now you should be able to test and demo the microservice in action and see real time event driven updates on the frontend-cli example program.

# 🐳 Docker

A ~10 MB multi-arch (`linux/amd64`, `linux/arm64`) image built from `scratch` and running as a non-root user is published on Docker Hub:

```shell
docker run --rm -p 8080:8080 \
  -e JWT_SECRET=your-jwt-secret \
  -e TIME_TOKEN_SECRET=your-time-token-secret \
  slincsilver/real-time-ui-update-microservice:latest
```

`GET /healthz` returns `200` once the server is accepting connections, for liveness/readiness probes. The process shuts down gracefully on `SIGTERM`, sending every client a `1001 going away` close frame.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | – (required) | HMAC secret for user JWTs (`HS256`/`HS384`/`HS512` only) |
| `TIME_TOKEN_SECRET` | – (required) | Shared secret for backend time-based tokens |
| `PORT` | `8080` | HTTP listen port |
| `TIME_WINDOW_SECONDS` | `3600` | Validity window of a time token, must be ≥ 1 |
| `ALLOWED_CLOCK_SKEW` | `1` | Neighbouring windows accepted on each side, must be ≥ 0 |
| `MAX_BODY_BYTES` | `1048576` | Maximum publish payload; larger requests get `413` |
| `ALLOWED_ORIGINS` | – (any) | Comma separated WebSocket `Origin` allowlist; requests without `Origin` (non-browser clients) are always allowed |
| `PPROF_ADDR` | – (off) | Address for an optional `net/http/pprof` listener, e.g. `127.0.0.1:6060`. Never expose it publicly |

Invalid values stop the service at startup with every problem reported at once.

# Usage

This section shows the production endpoints and the parameters a backend or client must provide to publish and receive channel-scoped updates.

### Backend -> Microservice (publish an update)

- Endpoint (private backend update):
	- POST /update?channel=<channel-name>
	- Headers:
		- X-API-Token: <time-token> (required) — the time-based token generated using the shared secret
		- Content-Type: application/json
	- Body: arbitrary JSON payload that will be broadcast to clients subscribed to the channel

- Endpoint (publish to public clients):
	- POST /publish?channel=<channel-name>
	- Headers: same as above (the endpoint is protected with the same time-token middleware so the request must originate from your backend)
	- Body: arbitrary JSON payload

Example curl (publish to channel "news"):

```bash
curl -X POST 'http://localhost:8080/publish?channel=news' \
	-H 'Content-Type: application/json' \
	-H 'X-API-Token: <TIME_TOKEN>' \
	-d '{"id":"order-123","item":"Demo","amount":10}'
```

Notes:
- The `channel` query parameter is optional; if omitted the server uses the `default` channel.
- The time-based token must be created by your backend using the shared secret (see `internal/auth/timetoken.go` and the demo backend simulator for a reference implementation).

### Client -> Microservice (subscribe to a channel)

- Authenticated client (user-specific):
	- WebSocket endpoint: `wss://<host>/ws?token=<JWT>&channel=<channel-name>`
	- The JWT token authenticates the user and the client will be added to the *authenticated* channel collection.

- Public client (no JWT):
	- WebSocket endpoint: `wss://<host>/ws/public?channel=<channel-name>`
	- No JWT required; the client will be added to the *public* channel collection.

Examples (browser / CLI WebSocket):

Authenticated (private channel "internal"):
```js
// Example URL (JS)
const ws = new WebSocket('wss://example.com/ws?token=JWT_HERE&channel=internal');
```

Public (public channel "news"):
```js
const ws = new WebSocket('wss://example.com/ws/public?channel=news');
```

Behavior summary:
- Clients subscribing with the same `channel` string will only receive messages published to that channel.
- Authenticated and public clients are isolated: authenticated broadcasts (from `/update`) only go to authenticated clients on that channel; public broadcasts (from `/publish`) only go to public clients on that channel.
- Every published payload is delivered as its own WebSocket text frame, byte for byte. Sequential publishes arrive in order.
- Messages sent by clients are discarded; client frames over 512 bytes close the connection with `1009`.
- Each client may have up to 4 MiB of undelivered payload queued. A client that falls further behind is disconnected with `1008 slow consumer` so it can never slow down other clients; it should reconnect and resynchronise.

If you use the demo tools, the frontend CLI and backend simulator expose these channel options interactively so you can test different channel configurations quickly.



# 🧪 Testing

```shell
make test        # unit tests with the race detector
make e2e         # end-to-end suites against a race-detector build
make e2e-image   # functional and security suites against the Docker image
make perf        # performance scenarios and optimization report
make bench       # micro benchmarks
```

- **Unit tests** cover every package, including time-token interoperability with the Node.js reference implementation, JWT algorithm-confusion attacks, body limits, origin policy, framing, ordering, ping/pong liveness, slow-consumer eviction and shutdown, plus a fuzz target for token parsing.
- **End-to-end tests** (`test/e2e`, build tag `e2e`) build the real binary with the race detector and coverage, boot it with throwaway secrets and drive it with simulated backends and browser users:
  - *functional*: fan-out, ordering, payload fidelity, channel and scope isolation, reconnects, graceful shutdown, configuration validation;
  - *security*: forged/expired/replayed tokens, scope escalation, oversized bodies and headers, origin allowlist, WebSocket protocol violations, slowloris, connection floods with goroutine-leak detection, no secrets or internals in error responses;
  - *concurrency*: concurrent publishers with exactly-once and per-publisher ordering checks, subscriber churn, slow consumers, shutdown under load. Any data race, panic or unclean exit reported by the server fails the run.
- **Performance suite** profiles the live server through pprof and writes `test/e2e/reports/perf-<label>.md` with latency percentiles, throughput, memory and goroutines per connection, CPU/allocation/heap hotspots and the optimization opportunities they point to. `E2E_BINARY` profiles another build and `E2E_PERF_BASELINE` adds a comparison column.

# ⚡ Performance

Driven by the performance suite's findings, measured on an Apple M3 Pro (11 cores) with the load generator on the same host:

| Metric | Before | After |
|--------|-------:|------:|
| Memory (RSS) per idle connection | 31.4 KiB | 16.0 KiB |
| Goroutines per idle connection | 2 | 1 |
| Multi-channel throughput (2000 users) | 237k msg/s | 237k msg/s |
| Multi-channel delivery latency p99 | 4.9 ms | 4.0 ms |
| Fan-out to 2000 users, delivery latency p50 / p99 | 4.5 / 11.7 ms | 4.5 / 10.8 ms |

Throughput is bound by kernel write syscalls (one per delivered frame), so the gains are in memory per connection, allocation pressure and tail latency; a broadcast performs no heap allocation. The earlier implementation also merged queued messages into a single frame, which produced invalid JSON under bursts; every message is now a separate frame.

### License
This project is licensed under the MIT License. See the LICENSE file for details.


