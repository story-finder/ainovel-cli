# AI Novel web host

This is the Release 1 operator guide for the standalone web host. It keeps one
`ainovel-web-host` process serving the browser controls and event stream over a
fixed LAN/VPN host port. The legacy TUI must not run against the same workspace
at the same time.

## Start the host

Prepare the mounted directories and configuration, then build and start the
container:

```bash
mkdir -p config workspace
docker compose up --build
```

Open the browser at `http://<LAN-IP>:8888`. Replace `<LAN-IP>` with the Docker
host's address reachable from the LAN or VPN.

To inspect the live event stream directly:

```bash
curl -N http://<LAN-IP>:8888/events
```

The `/status` endpoint is a lightweight health check and does not require
`api_key` or `base_url` query parameters.

## LAN/VPN reverse proxy

Forward the web host's HTTP routes to host port `8888`, which publishes the
container's port `8080`. Keep SSE buffering disabled with this exact Nginx
location:

```nginx
location /events {
    proxy_pass http://192.168.5.107:8888/events;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 1h;
}
```

The browser controls must remain reachable through the same Nginx LAN/VPN
reverse proxy, with `/events` handled by the block above.

## Operator checklist

1. Docker publishes host port 8888 to container port 8080.
2. GET /status returns 200 without api_key or base_url.
3. GET /events has Content-Type text/event-stream and X-Accel-Buffering no.
4. Browser controls work through an Nginx LAN/VPN reverse proxy.
5. Refreshing browser does not create a second Host process.

## Legacy TUI safety

Stop the web host before using the legacy TUI, and start it again afterward:

```bash
docker compose stop
docker compose run --rm --entrypoint ainovel-cli ainovel \
  --config /root/.ainovel/config.json
docker compose up --build
```

Browser refreshes only create a new HTTP client connection. They do not start a
second host process. This guide intentionally covers the direct LAN/VPN fixed-
port, one-process Release 1 behavior; it does not add Release 2 features.
