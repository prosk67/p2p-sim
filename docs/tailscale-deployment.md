# Deploying across real machines with Tailscale

This guide runs one node per physical machine, with all machines joined into
one Tailscale network (a "tailnet"). Tailscale gives the pool:

- WireGuard encryption between machines;
- reachability across NAT and different networks, with no port forwarding;
- access rules that decide which devices may talk to the nodes.

The application code does not change. Nodes find each other through
`peers.yaml`, so only addresses and bind settings differ from the
single-host Docker setup.

> **Scope.** Tailscale controls *who can reach* the pool. It does not fix the
> browser-side issues in [`security-audit.md`](security-audit.md) (M-1
> cross-site requests, M-2 DNS rebinding): a malicious site open in the
> browser of anyone on the tailnet can still start runs. Every node still
> trusts every other node's results, so only add machines you control.

## 1. Plan the layout

Example used below: three machines, one node each, and the gateway on the
first.

| Machine (tailnet name) | Tailscale IP | Runs | Published port |
|---|---|---|---|
| `lab-pc-1` | `100.64.0.1` | `node-1`, gateway | 8081, GUI 8090 |
| `lab-pc-2` | `100.64.0.2` | `node-2` | 8082 |
| `laptop`   | `100.64.0.3` | `node-3` | 8083 |

Use your own names and IPs (`tailscale ip -4` on each machine).

**Same CPU type everywhere.** If you mix x86 and ARM machines, the maths
library can round results differently. PDES and attack runs would then fail
their "identical to a single-process run" check, even though nothing is
broken.

## 2. Join the machines

On every machine:

1. Install Tailscale and Docker, and clone this repository.
2. Run `sudo tailscale up` and log in to the same tailnet.
3. Note the machine's address: `tailscale ip -4`.

Check that the machines reach each other **directly**:

```sh
tailscale ping lab-pc-2
# good:  pong from lab-pc-2 (100.64.0.2) via 192.168.1.20:41641 in 1ms
# bad:   pong from lab-pc-2 (100.64.0.2) via DERP(fra) in 38ms
```

"via DERP" means traffic goes through a Tailscale relay server. That works,
but it adds tens of milliseconds to every request.

- **M/M/1 and network batches:** these barely notice.
- **PDES and attack runs:** these wait for a round trip every lock-step
  window, so the speedup can disappear.

Machines on the same LAN almost always connect directly. If yours do not,
see [Tailscale's connection troubleshooting](https://tailscale.com/kb/1257/connection-types).

## 3. Shared peer list

All machines must use the same peer list. Copy the example and fill in the
Tailscale IPs:

```sh
cp config/peers.tailnet.example.yaml config/peers.tailnet.yaml
```

```yaml
peers:
  - id: node-1
    url: http://100.64.0.1:8081
  - id: node-2
    url: http://100.64.0.2:8082
  - id: node-3
    url: http://100.64.0.3:8083
```

Copy the finished file to every machine. It is git-ignored, so your
addresses stay out of the repository.

**Use IPs, not MagicDNS names.** The nodes run inside containers, and
Docker's internal DNS usually does not forward to Tailscale's resolver.
`http://lab-pc-2:8082` may resolve on the host but fail inside a node.

## 4. Per-machine settings

Each machine gets its own `.env` in the repository root. Compose reads it
automatically, and it is git-ignored.

```sh
cp .env.example .env
```

Set `NODE_BIND` to **that machine's** Tailscale IP. Ports are then
published only on the Tailscale interface, never on the LAN or the internet.
This matters because Docker-published ports bypass host firewalls such as
ufw.

```sh
# .env on lab-pc-2
NODE_BIND=100.64.0.2
```

## 5. Start the nodes

On each machine, start **only its own node**. The tailnet override points the
nodes at `peers.tailnet.yaml`:

```sh
# lab-pc-1
docker compose -f docker-compose.yml -f docker-compose.tailnet.yml up -d --build node-1
# lab-pc-2
docker compose -f docker-compose.yml -f docker-compose.tailnet.yml up -d --build node-2
# laptop
docker compose -f docker-compose.yml -f docker-compose.tailnet.yml up -d --build node-3
```

If `up` fails with `cannot assign requested address`, Tailscale was not
connected when Docker tried to publish the port. Run `tailscale up`, then
start the node again. After a reboot, start Tailscale before the
containers.

## 6. Start the gateway

On the gateway machine (`lab-pc-1`), add this to its `.env` so the gateway
reads the same peer list:

```sh
GATEWAY_PEERS=peers.tailnet.yaml
```

Keep the GUI on loopback and share it over HTTPS with `tailscale serve`:

```sh
docker compose -f docker-compose.yml -f docker-compose.web.yml up -d --build gateway
tailscale serve --bg 8090
tailscale serve status        # shows https://lab-pc-1.<your-tailnet>.ts.net
```

Anyone on the tailnet can now open that URL. Nobody outside it can. Undo it
with `tailscale serve reset`.

To use plain HTTP on the tailnet instead, set `GUI_BIND` in that machine's
`.env` to its Tailscale IP and open `http://100.64.0.1:8090`.

## 7. Restrict access with a tailnet policy

By default every device on a tailnet can reach every other. Tag the pool
machines and allow only what the pool needs:

- nodes and the gateway may reach the node ports;
- people may reach the gateway only.

To tag a machine, use the admin console (Machines → ⋯ → Edit ACL tags) or
run `sudo tailscale up --advertise-tags=tag:p2p-node`. Repeat any flags you
used with `up` before.

Give the gateway machine both tags: it runs a node and the gateway.

Policy additions (merge these into your existing policy file):

```jsonc
{
  "tagOwners": {
    "tag:p2p-node":    ["autogroup:admin"],
    "tag:p2p-gateway": ["autogroup:admin"]
  },
  "acls": [
    // Nodes call each other (tasks, PDES steps, health); the gateway calls nodes.
    { "action": "accept", "src": ["tag:p2p-node", "tag:p2p-gateway"], "dst": ["tag:p2p-node:8081-8084"] },
    // People reach only the dashboard (443 via tailscale serve, 8090 if bound directly).
    { "action": "accept", "src": ["autogroup:member"], "dst": ["tag:p2p-gateway:443,8090"] }
  ]
}
```

A policy with an `acls` section denies everything it does not allow. If your
tailnet still uses the default allow-all rule, keep the rules you rely on
(SSH, file sharing) alongside these.

## 8. Verify

1. **Host to host:** from any machine, `curl http://100.64.0.2:8082/health`
   returns `{"status":"ok","node_id":"node-2",...}`.
2. **Container to remote node.** This is the path batches actually use:
   ```sh
   docker exec node-1 python3 -c "import urllib.request as u; print(u.urlopen('http://100.64.0.2:8082/health').read())"
   ```
3. **Pool view:** the gateway's Cluster page shows every node healthy and
   the "Who sees whom" matrix fully green. Each row's resource meters now
   describe a separate machine.
4. **A real run:** launch a small M/M/1 batch from the GUI. The report
   should list tasks on every node.
5. **Policy check:** from a machine *without* the tags, `curl` to a node port
   should time out.

## 9. Adding or removing a machine

Membership is static.

1. Add the new node to `peers.tailnet.yaml` on **every** machine and the
   gateway, then restart all nodes and the gateway.
2. For a fourth node, also add a `node-4` entry to `docker-compose.tailnet.yml`
   and start it with `docker-compose.node4.yml` included.

Nodes do not reload the peer list while running.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Node healthy from the host, unreachable from other nodes | Peer URL uses a MagicDNS name (use the IP), or a policy rule is missing |
| `cannot assign requested address` on `up` | Tailscale not connected yet, or `NODE_BIND` is not this machine's Tailscale IP |
| PDES slower than one node | Relayed (DERP) connection; check `tailscale ping` |
| PDES or attack run fails its identical-result check | Machines with different CPU types or images; use the same image everywhere |
| Gateway shows a node as down after its laptop slept | Expected; the node's batches re-dispatch elsewhere, and it rejoins when awake |
