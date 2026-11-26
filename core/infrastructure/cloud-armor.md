# Cloud Armor WAF & Edge Rate-Limiting Configuration

Nori layers **in-app per-user rate limiting** (token buckets + concurrent SSE caps in `core/internal/api/ratelimit.go` and BFF IP rate limiting in `web/src/lib/runtime/rate-limit.ts`) with **GCP Cloud Armor** at the External HTTPS Load Balancer edge.

## Topology

- **`nori-web`** serves `noribot.chat` through a Cloud Run domain mapping, behind IAP. **`nori-core`** is public on its `run.app` URL and accepts only HS256 service tokens. Neither has a load balancer or a Cloud Armor policy.
- **`nori-connector-relay`** (Home AI) is the only service behind a load balancer. Its backend uses the `nori-home-ai-throttle` policy, which limits each client IP to 60 requests per minute. [`infra/home_ai_edge.tf`](../../infra/home_ai_edge.tf) defines both.
- **`nori-worker`** has internal ingress and no public route.
- The `nori-edge-waf` policy below is a proposal. Using it needs a load balancer with a serverless NEG in front of `nori-web` and `nori-core`, added to `infra/`.

## Proposed Cloud Armor Policy (`nori-edge-waf`)

### 1. Create the security policy

```bash
gcloud compute security-policies create nori-edge-waf \
  --project="${GCP_PROJECT:-bot-111549}" \
  --description="Nori edge WAF and IP rate limiting"
```

### 2. Enable Adaptive Protection (L7 DDoS defense)

```bash
gcloud compute security-policies update nori-edge-waf \
  --project="${GCP_PROJECT:-bot-111549}" \
  --enable-layer7-ddos-defense
```

### 3. Strict IP throttle on user resolution / auth endpoints

Protects `/api/users/resolve` and auth callbacks against credential stuffing and email enumeration (60 req/min per IP -> HTTP `429`):

```bash
gcloud compute security-policies rules create 1000 \
  --project="${GCP_PROJECT:-bot-111549}" \
  --security-policy=nori-edge-waf \
  --expression="request.path.matches('^/api/(users/resolve|auth/.*)$')" \
  --action=throttle \
  --rate-limit-threshold-count=60 \
  --rate-limit-threshold-interval-sec=60 \
  --conform-action=allow \
  --exceed-action=deny-429 \
  --enforce-on-key=IP
```

### 4. Global per-IP rate limit for general web/API traffic

Caps abusive clients before they consume Cloud Run concurrency (600 req/min per IP -> HTTP `429`):

```bash
gcloud compute security-policies rules create 2000 \
  --project="${GCP_PROJECT:-bot-111549}" \
  --security-policy=nori-edge-waf \
  --src-ip-ranges="*" \
  --action=throttle \
  --rate-limit-threshold-count=600 \
  --rate-limit-threshold-interval-sec=60 \
  --conform-action=allow \
  --exceed-action=deny-429 \
  --enforce-on-key=IP
```

### 5. Preconfigured OWASP ModSecurity CRS WAF rules

Start in `--preview` mode to verify zero false positives on chat/markdown payloads, then remove `--preview` to enforce:

```bash
gcloud compute security-policies rules create 3000 \
  --project="${GCP_PROJECT:-bot-111549}" \
  --security-policy=nori-edge-waf \
  --expression="evaluatePreconfiguredWaf('sqli-v33-stable', {'sensitivity': 1}) || evaluatePreconfiguredWaf('lfi-v33-stable', {'sensitivity': 1}) || evaluatePreconfiguredWaf('rce-v33-stable', {'sensitivity': 1}) || evaluatePreconfiguredWaf('protocolattack-v33-stable', {'sensitivity': 1})" \
  --action=deny-403 \
  --preview
```

> Note: Keep `xss-v33-stable` at sensitivity 1 or preview-only on chat message endpoints (`/v1/conversations/assistant/messages`) because users routinely paste HTML/code snippets into prompts.

### 6. Attach policy to the Serverless NEG backend service

```bash
gcloud compute backend-services update nori-web-backend \
  --project="${GCP_PROJECT:-bot-111549}" \
  --global \
  --security-policy=nori-edge-waf
```

## Complementing In-App Limits

| Layer | Key | Scope | Default Limit |
| :--- | :--- | :--- | :--- |
| Cloud Armor (proposed) | Client IP | `/api/users/resolve`, `/api/auth/*` | 60 req/min |
| Cloud Armor (proposed) | Client IP | All public routes | 600 req/min |
| Web BFF (`rate-limit.ts`) | Client IP | Pre-auth proxy requests | 120 req/min (burst 30) |
| Core API (`ratelimit.go`) | Client IP & email hash | `POST /api/users/resolve` | 30 req/min (burst 10) |
| Core API (`ratelimit.go`) | `user.ID` | Writes / mutations | 60 req/min (burst 20) |
| Core API (`ratelimit.go`) | `user.ID` | Uploads | 20 req/min (burst 10) |
| Core API (`ratelimit.go`) | `user.ID` | Reads / polling (`GET`) | 300 req/min (burst 60) |
| Core API (`ratelimit.go`) | `user.ID` | Concurrent SSE streams | 10 / user / instance |
