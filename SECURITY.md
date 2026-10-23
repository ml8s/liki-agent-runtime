# Security Policy

`liki-agent-runtime` is an internal runtime and must not be exposed directly to the
public internet.

Required controls:

- Internal bearer token between the gateway and `liki-agent-runtime` outside local
  development.
- The same internal token for protocol calls and `/metrics`.
- Verified user context supplied by the gateway for AG-UI.
- The browser identity header is adopted on AG-UI only; A2A machine callers
  sharing the internal token cannot spoof a user identity.
- Internet-facing request rate limiting is owned by the gateway; the runtime's
  internal per-source limiter only resists token brute force.
- Bounded per-source throttling of invalid bearer or identity attempts.
- Deployment integrity is owned by liki-deploy, which assembles the images by
  commit sha; the runtime keeps no duplicate artifact pin.
- HTTPS-only LLM endpoints outside development and no credentials embedded in
  configured URLs.
- Explicit per-Agent MCP tool allowlists.
- No secrets, prompts, raw model output, or tool payloads in logs.
- Least-privilege, non-root deployment.

## Reporting a vulnerability

Do not open a public issue for a security vulnerability. Use GitHub's private
vulnerability reporting for this repository.
