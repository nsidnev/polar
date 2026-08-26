# Backoffice proxy

Publishes the Polar backoffice as a [Tailscale service][ts-services], served from
a long-running container on Vercel (a "blackbox"). The backoffice is not reachable
from the public internet: requests that don't arrive through this proxy get a 404.

> Blackbox is in Vercel private alpha. Provisioning is a raw API call and the
> controller build is pinned by hand. Expect rough edges.

## How it works

```
tailnet client (see "Reaching it" below)
  │  http://polar-backoffice.<tailnet>.ts.net/api/backoffice/
  ▼
Tailscale service  svc:polar-backoffice
  │
  ▼
this proxy (blackbox, no public ingress)
  │  path forwarded verbatim, plus:
  │    X-Polar-Backoffice-Token   the box's Vercel OIDC token
  │    X-Polar-Backoffice-User    tailnet user, from WhoIs
  │    X-Polar-Forwarded-Host     this service's own FQDN
  │    x-vercel-protection-bypass deployment protection bypass
  │  Host stays the deployment's — Vercel routes on it
  ▼
Polar API (Vercel function)
  ├─ verifies the token's issuer/audience/subject, else 404
  ├─ resolves the operator by email, requires is_admin
  └─ renders URLs against the forwarded host, so links stay on the tailnet
```

Two independent uses of the same OIDC token: joining the tailnet (workload
identity federation) and proving to the API that a request came from this box.
Being on the tailnet is not sufficient — the API still requires an admin user.

The API side lives in `server/polar/backoffice/tailnet.py` and is inert unless
`POLAR_BACKOFFICE_PROXY_OIDC_ISSUER` is set.

## Configuration

Everything is read from the environment. Project environment variables reach the
box, but **Vercel's own system variables do not** — `VERCEL_PROJECT_PRODUCTION_URL`
and `VERCEL_AUTOMATION_BYPASS_SECRET` are absent even when the project has them,
so set `POLAR_UPSTREAM_URL` and re-add the bypass secret as ordinary project
variables. The box logs which values it resolved on startup.

| Variable | Default | |
|---|---|---|
| `TS_CLIENT_ID` | — | Federated identity client ID. Preferred. |
| `TS_AUTHKEY` | — | Fallback when the tailnet doesn't trust the Vercel issuer. |
| `TS_SERVICE_NAME` | `svc:polar-backoffice` | `svc:` prefix added if missing. |
| `TS_TAGS` | `tag:polar-backoffice` | Comma-separated. |
| `TS_HOSTNAME` | `polar-backoffice-proxy[-<ordinal>]` | |
| `TS_TRUSTED_CLIENT_TAGS` | — | Tagged peers allowed to connect. See "Reaching it". |
| `TS_OPERATOR` | — | Attribute every authorized peer to this admin. |
| `TS_PORT` | `80` | Must match the service definition. |
| `TS_STATE_DIR` | `/tmp/tsnet` | Ephemeral by design; no drive is mounted. |
| `POLAR_UPSTREAM_URL` | from `VERCEL_PROJECT_PRODUCTION_URL` | |
| `BACKOFFICE_PATH_PREFIX` | `/api/backoffice` | |
| `VERCEL_AUTOMATION_BYPASS_SECRET` | — | Required if deployment protection is on. |
| `VERCEL_OIDC_TOKEN_PATH` | `/var/run/secrets/vercel.com/token` | Falls back to `VERCEL_OIDC_TOKEN`. |

`VERCEL_URL` is deliberately unused: it names one deployment, and a box outlives
every one of them.

## Tailnet setup

Configure this in the admin console, not Terraform: the `tailscale_acl` resource
owns the entire policy document, so an apply is a whole-file replace.

1. **Trust credential** (Settings → Trust credentials → OpenID Connect → custom
   issuer)
   - Issuer `https://oidc.vercel.com/<team-slug>`
   - Subject `owner:<team>:project:<project>:environment:production`
   - Scopes must include `auth_keys`
   - Tag `tag:polar-backoffice`
   - Copy the client ID into `TS_CLIENT_ID`

   Tailscale defaults the expected audience to `api.tailscale.com/<client-id>`,
   which a blackbox token will never carry: it gets Vercel's default
   `https://vercel.com/<team>`, and the audience option in `@vercel/oidc` is a
   runtime exchange that isn't available inside a box. Edit the credential's
   audience to match the token instead. Until it matches, registration fails
   with:

   ```
   tsnet.Up: error resolving auth key: token exchange failed with status 403
   ```

   and the credential's page in the admin console gives the reason
   ("token has invalid audience").

   The box logs `iss`, `aud` and `sub` on startup — use those values for both the
   credential and `POLAR_BACKOFFICE_PROXY_*`. If the audience can't be reconciled,
   `TS_AUTHKEY` is the fallback; the API leg is unaffected either way.

2. **Service** (Services → Define a Service): name `polar-backoffice`, port
   `tcp:80`.

   A service only gets a VIP once it is defined here. `autoApprovers` and
   `grants` can reference `svc:polar-backoffice` before it exists and the policy
   file still saves, and `ListenService` builds its FQDN locally, so the logs
   look healthy while nothing resolves. The box advertises once at startup, so
   restart it if the service is defined afterwards.

3. **MagicDNS** (DNS → enable MagicDNS). A service is reached by its MagicDNS
   name, so with MagicDNS off the name never resolves. Peer node names still
   resolve from the host table, which makes this look like a service problem
   rather than a tailnet DNS setting.

4. **Policy file**

   ```json
   "tagOwners":     { "tag:polar-backoffice": ["autogroup:member"] },
   "autoApprovers": { "services": { "svc:polar-backoffice": ["tag:polar-backoffice"] } },
   "grants": [
     { "src": ["autogroup:member"], "dst": ["svc:polar-backoffice"], "ip": ["80"] }
   ]
   ```

   `autoApprovers` matters: without it every restart leaves the service
   advertisement pending admin approval.

Clients need Tailscale 1.94+ to reach services without `--accept-routes`.

## Reaching it

Two separate questions, and it helps to keep them apart.

**May this peer connect?** Settled by the Tailscale grants, plus
`TS_TRUSTED_CLIENT_TAGS` for tagged nodes. Tagged nodes are refused by default.

**Which Polar admin is this?** A tailnet login and a Polar account are different
things, and the proxy will not guess. By default an untagged node's login name is
used, which only works when tailnet logins are the same addresses as Polar
accounts. `TS_OPERATOR` overrides that and attributes every authorized peer to
one admin. It is required for tagged peers, which have no identity to forward.

Whatever is resolved has to match a Polar user with `is_admin`, or the request
gets the same 404 as an unauthenticated one.

### A person's own machine

Nothing to configure if their tailnet login is their Polar account:

```sh
# no TS_OPERATOR, no TS_TRUSTED_CLIENT_TAGS
```

On a personal tailnet, logins usually aren't Polar accounts, so set
`TS_OPERATOR` to the admin to act as.

### A bastion

Where Tailscale can't be installed on managed laptops, a
[Vercel Sandbox][vercel-sandbox] joins the tailnet and forwards from there.
Workload identity federation requires tagged nodes, so it must be nominated:

```sh
TS_TRUSTED_CLIENT_TAGS=tag:polar-backoffice-client
TS_OPERATOR=admin@example.com
```

Add the tag to the policy file's `tagOwners` and grant it the service:

```json
"grants": [
  { "src": ["tag:polar-backoffice-client"], "dst": ["svc:polar-backoffice"], "ip": ["80"] }
]
```

Then, from inside the sandbox,
`curl http://polar-backoffice.<tailnet>.ts.net/api/backoffice/` reaches the
backoffice while the same path on the public deployment returns 404.

Everything through a bastion is attributed to one admin, so the API records the
bastion's operator rather than whoever was sitting at it. That is a real loss of
accountability next to per-user identity — keep the tag reserved for bastions and
grant it narrowly.

Publishing the sandbox's port (`sandbox create --publish-port`) makes the
backoffice reachable in a browser at a `*.vercel.run` URL. That URL is
unauthenticated: it re-introduces exactly the public entry point this design
removes. Don't use it for anything but a throwaway demo, and never point it at
real data.

[vercel-sandbox]: https://vercel.com/docs/sandbox

### Why plain HTTP

The node is ephemeral and its state directory is thrown away on restart, so HTTPS
would re-issue a certificate on every boot and hit Let's Encrypt's
duplicate-certificate limit during any normal iteration loop. Nothing here relies
on browser cookies — the operator's identity comes from the tailnet — so this
costs no security on a private network.

To switch: `ServiceModeHTTP{HTTPS: true, Port: 443}` in `main.go`, service port
`tcp:443`, grant `ip: ["443"]`, and HTTPS certificates enabled on the tailnet.

## Running locally

Proves the whole path without involving blackbox at all:

```sh
vercel env pull                      # provides VERCEL_OIDC_TOKEN
export TS_CLIENT_ID=...              # or TS_AUTHKEY
export POLAR_UPSTREAM_URL=https://<deployment-host>
export VERCEL_AUTOMATION_BYPASS_SECRET=...
just run
```

The startup log prints the service URL to open.

## Deploying

```sh
just deploy 1     # vet, test, build+push to VCR, configure the box
just stop         # scale to zero, keeping the configuration
```

Logs go to Datadog (`just logs` prints the query). Boxes are bound to an
environment rather than a deployment, so this does not roll with `vercel deploy`.

[ts-services]: https://tailscale.com/kb/1554/tailscale-services
