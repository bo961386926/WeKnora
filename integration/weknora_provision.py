#!/usr/bin/env python3
"""Provision one WeKnora workspace for a SaaS tenant.

Creates the space, its models, the quick-answer agent binding, a knowledge
base and a tenant-scoped API key, then prints a mapping record.

Usage:
    export WEKNORA_BASE_URL=https://kb.qh-cloud.com
    export WEKNORA_ADMIN_EMAIL=provisioner@example.com
    export WEKNORA_ADMIN_PASSWORD='...'

    python3 weknora_provision.py \
        --name "客户A" \
        --chat-base-url http://qh-office.qh-cloud.com:1001/v1 \
        --chat-model Qwen3-30B-A3B-Q4_K_M.gguf \
        --embed-base-url http://qh-office.qh-cloud.com:1002/v1 \
        --embed-model tencent/WeMM-Embedding-2B \
        --embed-dim 2048

The account only needs to be able to create workspaces (a normal account with
self-service creation enabled works; it becomes Owner of every workspace it
creates). Raise its per-user workspace quota when provisioning many tenants.

Re-running is idempotent for the space, models and knowledge base. API keys
are write-once: an existing key's plaintext cannot be read back, so a rerun
creates a new key unless one with the same name already exists.
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("WEKNORA_BASE_URL", "https://kb.qh-cloud.com").rstrip("/")
EMAIL = os.environ.get("WEKNORA_ADMIN_EMAIL", "")
PASSWORD = os.environ.get("WEKNORA_ADMIN_PASSWORD", "")

QUICK_ANSWER_AGENT = "builtin-quick-answer"


def request(method, path, body=None, token=None, tenant_id=None, api_key=None, timeout=60):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if api_key:
        req.add_header("X-API-Key", api_key)
    if tenant_id:
        req.add_header("X-Tenant-ID", str(tenant_id))
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode()
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode(errors="replace")
        raise RuntimeError(f"{method} {path} -> HTTP {exc.code}: {raw[:600]}") from None
    return json.loads(raw) if raw else {}


def payload(resp):
    if isinstance(resp, dict) and "data" in resp:
        return resp["data"]
    return resp


def as_list(value):
    """List endpoints return either a bare array or an object with `items`."""
    if isinstance(value, list):
        return value
    if isinstance(value, dict):
        for key in ("items", "list", "tenants", "models", "knowledge_bases"):
            if isinstance(value.get(key), list):
                return value[key]
    return []


def unwrap(value, key):
    """Create endpoints may nest the created row under a named key."""
    if isinstance(value, dict) and isinstance(value.get(key), dict):
        return value[key]
    return value


def login():
    resp = payload(request("POST", "/api/v1/auth/login", {"email": EMAIL, "password": PASSWORD}))
    token = resp.get("token") or resp.get("Token")
    if not token:
        raise RuntimeError(f"login returned no token: {json.dumps(resp)[:400]}")
    return token


def find_tenant(token, name):
    # GET /tenants only returns the active workspace; the membership list on
    # /auth/me is what spans every workspace this account owns.
    me = payload(request("GET", "/api/v1/auth/me", token=token))
    for membership in me.get("memberships") or []:
        if membership.get("tenant_name") == name:
            return {"id": membership["tenant_id"], "name": name}
    return None


def ensure_tenant(token, name, description, reuse):
    if reuse:
        existing = find_tenant(token, name)
        if existing:
            print(f"space: reusing #{existing['id']} {name}")
            return int(existing["id"])
    tenant = unwrap(payload(request("POST", "/api/v1/tenants",
                                    {"name": name, "description": description}, token=token)), "tenant")
    tenant_id = int(tenant["id"])
    print(f"space: created #{tenant_id} {name}")
    return tenant_id


def ensure_model(token, tenant_id, name, model_type, base_url, model_name, dimension=None, reuse=True):
    if reuse:
        for model in as_list(payload(request("GET", "/api/v1/models", token=token, tenant_id=tenant_id))):
            if model.get("name") == name and model.get("type") == model_type:
                print(f"model: reusing {name} ({model_type}) id={model['id']}")
                return model["id"]
    parameters = {
        "base_url": base_url,
        "provider": "generic",
        "interface_type": "openai",
    }
    if dimension:
        parameters["embedding_parameters"] = {"dimension": dimension}
    body = {
        "name": name,
        "display_name": name,
        "type": model_type,
        "source": "remote",
        "description": f"provisioned for {model_type}",
        "parameters": parameters,
    }
    model = unwrap(payload(request("POST", "/api/v1/models", body, token=token, tenant_id=tenant_id)), "model")
    print(f"model: created {name} ({model_type}) id={model['id']}")
    return model["id"]


def bind_quick_answer_agent(token, tenant_id, chat_model_id):
    agent = unwrap(payload(request("GET", f"/api/v1/agents/{QUICK_ANSWER_AGENT}",
                                    token=token, tenant_id=tenant_id)), "agent")
    config = agent.setdefault("config", {})
    if config.get("model_id") == chat_model_id:
        print(f"agent: {QUICK_ANSWER_AGENT} already bound to {chat_model_id}")
        return
    config["model_id"] = chat_model_id
    request("PUT", f"/api/v1/agents/{QUICK_ANSWER_AGENT}", agent,
            token=token, tenant_id=tenant_id)
    print(f"agent: {QUICK_ANSWER_AGENT} bound to chat model {chat_model_id}")


def ensure_kb(token, tenant_id, name, embedding_model_id):
    for kb in as_list(payload(request("GET", "/api/v1/knowledge-bases",
                                      token=token, tenant_id=tenant_id))):
        if kb.get("name") == name:
            print(f"kb: reusing {name} id={kb['id']}")
            return kb["id"]
    body = {"name": name, "description": "", "type": "document",
            "embedding_model_id": embedding_model_id}
    kb = unwrap(payload(request("POST", "/api/v1/knowledge-bases", body,
                                token=token, tenant_id=tenant_id)), "knowledge_base")
    print(f"kb: created {name} id={kb['id']}")
    return kb["id"]


def ensure_api_key(token, tenant_id, name, kb_id):
    existing = as_list(payload(request("GET", f"/api/v1/tenants/{tenant_id}/api-keys",
                                       token=token, tenant_id=tenant_id)))
    for key in existing:
        if key.get("name") == name:
            print(f"key: '{name}' already exists; plaintext is not recoverable, "
                  f"delete it in the UI and rerun if you need a new one")
            return None
    body = {
        "name": name,
        "full_access": False,
        "knowledge_base_ids": [kb_id],
        "capabilities": ["retrieve", "chat", "ingest", "manage_kbs", "message_history"],
    }
    created = payload(request("POST", f"/api/v1/tenants/{tenant_id}/api-keys",
                              body, token=token, tenant_id=tenant_id))
    print(f"key: created '{name}'")
    # `api_key` is the encrypted value stored at rest (enc:v1...); the usable
    # plaintext token is returned once in `token`.
    return created.get("token") or created.get("api_key")


def smoke_test(api_key, tenant_id, kb_id):
    session = unwrap(payload(request("POST", "/api/v1/sessions", {"title": "provision-smoke"},
                                      api_key=api_key, tenant_id=tenant_id)), "session")
    session_id = session["id"]
    body = {"query": "你好，请用一句话回复。",
            "knowledge_base_ids": [kb_id],
            "agent_id": QUICK_ANSWER_AGENT}
    req = urllib.request.Request(
        BASE + f"/api/v1/knowledge-chat/{session_id}",
        data=json.dumps(body).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-API-Key", api_key)
    req.add_header("X-Tenant-ID", str(tenant_id))
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            chunk = resp.read(2000).decode(errors="replace")
        ok = "data:" in chunk or "event:" in chunk
        print(f"smoke: chat stream {'ok' if ok else 'unexpected'} ({len(chunk)} bytes)")
        return ok
    except urllib.error.HTTPError as exc:
        print(f"smoke: chat failed HTTP {exc.code}: {exc.read().decode(errors='replace')[:400]}")
        return False


def main():
    parser = argparse.ArgumentParser(description="Provision a WeKnora workspace for one SaaS tenant")
    parser.add_argument("--name", required=True, help="SaaS tenant name; becomes the workspace name")
    parser.add_argument("--description", default="")
    parser.add_argument("--kb-name", default="知识库")
    parser.add_argument("--chat-base-url", required=True)
    parser.add_argument("--chat-model", required=True)
    parser.add_argument("--chat-model-name", default=None, help="Name stored in WeKnora (defaults to --chat-model)")
    parser.add_argument("--embed-base-url", required=True)
    parser.add_argument("--embed-model", required=True)
    parser.add_argument("--embed-dim", type=int, required=True)
    parser.add_argument("--no-reuse", action="store_true", help="Create new rows even if same names exist")
    parser.add_argument("--smoke-test", action="store_true")
    parser.add_argument("--output", help="Write the mapping record to this JSON file")
    args = parser.parse_args()

    if not EMAIL or not PASSWORD:
        sys.exit("set WEKNORA_ADMIN_EMAIL and WEKNORA_ADMIN_PASSWORD")

    token = login()
    tenant_id = ensure_tenant(token, args.name, args.description, not args.no_reuse)
    chat_model_id = ensure_model(
        token, tenant_id, args.chat_model_name or args.chat_model, "KnowledgeQA",
        args.chat_base_url, args.chat_model, reuse=not args.no_reuse)
    embed_model_id = ensure_model(
        token, tenant_id, args.embed_model, "Embedding",
        args.embed_base_url, args.embed_model, args.embed_dim, reuse=not args.no_reuse)
    bind_quick_answer_agent(token, tenant_id, chat_model_id)
    kb_id = ensure_kb(token, tenant_id, args.kb_name, embed_model_id)
    api_key = ensure_api_key(token, tenant_id, f"saas-{args.name}", kb_id)

    smoke_ok = None
    if args.smoke_test and api_key:
        smoke_ok = smoke_test(api_key, tenant_id, kb_id)

    record = {
        "saas_tenant": args.name,
        "weknora_tenant_id": tenant_id,
        "kb_id": kb_id,
        "chat_model_id": chat_model_id,
        "embedding_model_id": embed_model_id,
        "api_key": api_key,
        "smoke_test_ok": smoke_ok,
    }
    print(json.dumps(record, ensure_ascii=False, indent=2))
    if args.output:
        with open(args.output, "w", encoding="utf-8") as fh:
            json.dump(record, fh, ensure_ascii=False, indent=2)
        print(f"mapping written to {args.output}")


if __name__ == "__main__":
    main()
