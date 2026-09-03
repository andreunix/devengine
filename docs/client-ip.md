# Client IP resolution

Forwarding headers are attacker-controlled unless the service receives them
through a trusted network boundary. `httpx/clientip` therefore ignores every
configured header when `RemoteAddr` is outside the configured proxy CIDRs.

## Cloudflare through a reverse proxy

For this path:

```text
browser -> Cloudflare -> reverse proxy -> devengine service
```

Configure only the CIDR used by the reverse proxy to connect to the service.
The reverse proxy must remove or overwrite the selected headers. Do not add
arbitrary networks merely because they may appear inside a forwarded chain.

The first present header wins. If it is malformed, resolution fails closed to
the direct peer instead of consulting a lower-priority header. Chain headers
are validated completely and walked from right to left; the nearest untrusted
hop is the client boundary.

`Forwarded` accepts literal IPv4 nodes and quoted, bracketed IPv6 nodes, with
an optional numeric port. `unknown`, obfuscated nodes, duplicate `for`
parameters, invalid IPs, empty hops, and oversized chains are rejected.
`CF-Connecting-IP` and `X-Real-IP` must each contain exactly one IP value.

The resolver is immutable after construction and safe for concurrent use.
Use the deprecated legacy functions only while migrating existing v0.2
consumers.
