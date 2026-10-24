#!/usr/bin/env python3
"""Mint a short-lived SSO token for WeKnora and optionally exchange it.

The token format is the contract your SaaS backend implements:

    header  { "alg": "HS256", "typ": "JWT" }
    payload {
      "iss":    <WEKNORA_SSO_ISSUER>,
      "aud":    <WEKNORA_SSO_AUDIENCE>,   // default "weknora"
      "sub":    <stable SaaS user id>,
      "email":  <user email>,
      "name":   <display name>,
      "tenant": <WeKnora workspace id>,
      "role":   "viewer" | "contributor" | "admin",
      "jti":    <unique, single use>,
      "iat":    <now>,
      "exp":    <now + ttl, <= 120s>
    }

Usage:
    export WEKNORA_SSO_SECRET=...        # same value as on the server
    export WEKNORA_SSO_ISSUER=qh-saas
    export WEKNORA_SSO_AUDIENCE=weknora

    python3 mint_sso_token.py --email user@example.com --tenant 10002

    # exchange it immediately and print the login result
    python3 mint_sso_token.py --email user@example.com --tenant 10002 --exchange
"""

import argparse
import base64
import hashlib
import hmac
import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid


def b64url(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b'=').decode()


def sign(secret: str, header: dict, payload: dict) -> str:
    segments = [
        b64url(json.dumps(header, separators=(',', ':'), sort_keys=True).encode()),
        b64url(json.dumps(payload, separators=(',', ':'), sort_keys=True).encode()),
    ]
    signing_input = '.'.join(segments).encode()
    signature = hmac.new(secret.encode(), signing_input, hashlib.sha256).digest()
    return '.'.join(segments + [b64url(signature)])


def main() -> None:
    parser = argparse.ArgumentParser(description="Mint a WeKnora SSO token")
    parser.add_argument("--email", required=True)
    parser.add_argument("--tenant", required=True,
                        help="WeKnora workspace id (numeric) or external tenant reference (any other string)")
    parser.add_argument("--role", default="viewer", choices=["viewer", "contributor", "admin"])
    parser.add_argument("--name", default="")
    parser.add_argument("--subject", default="", help="stable SaaS user id (defaults to email)")
    parser.add_argument("--ttl", type=int, default=60, help="token lifetime in seconds (max 120)")
    parser.add_argument("--exchange", action="store_true", help="POST the token and print the login result")
    args = parser.parse_args()

    secret = os.environ.get("WEKNORA_SSO_SECRET", "")
    if len(secret) < 16:
        sys.exit("WEKNORA_SSO_SECRET is required")
    issuer = os.environ.get("WEKNORA_SSO_ISSUER", "").strip()
    audience = os.environ.get("WEKNORA_SSO_AUDIENCE", "weknora").strip() or "weknora"

    now = int(time.time())
    ttl = max(1, min(args.ttl, 120))
    payload = {
        "iss": issuer,
        "aud": audience,
        "sub": args.subject or args.email,
        "email": args.email,
        "name": args.name or args.email.split("@")[0],
        "tenant": int(args.tenant) if args.tenant.isdigit() else args.tenant,
        "role": args.role,
        "jti": str(uuid.uuid4()),
        "iat": now,
        "exp": now + ttl,
    }
    token = sign(secret, {"alg": "HS256", "typ": "JWT"}, payload)
    print(token)

    if not args.exchange:
        return
    base = os.environ.get("WEKNORA_BASE_URL", "https://kb.qh-cloud.com").rstrip("/")
    req = urllib.request.Request(
        base + "/api/v1/auth/sso/exchange",
        data=json.dumps({"token": token}).encode(),
        method="POST",
    )
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            body = json.load(resp)
        user = body.get("user") or {}
        tenant = body.get("active_tenant") or {}
        print(f"exchange: ok user={user.get('email')} tenant={tenant.get('id')} "
              f"token_len={len(body.get('token') or '')}")
    except urllib.error.HTTPError as exc:
        print(f"exchange: HTTP {exc.code} {exc.read().decode(errors='replace')[:300]}")


if __name__ == "__main__":
    main()
