# SaaS 受管知识库集成计划（SSO + 工作空间自动开通）

> **状态更新（2026-10-24，二次）**：P1（任务 1）已落地：`tenant_bindings` 迁移 000119、
> managed-tenants API（platform key 网关）、SSO 外部引用解析（`WEKNORA_SSO_EXTERNAL_SOURCE`，
> 默认 qh-saas；数字 ID 兼容回退）。成员同步 DELETE 已含；PUT 成员 upsert 属任务 2（P2）。
> 
> **状态更新（2026-10-24）**：任务 0 已由 `company/v0.8.3` 分支上的实现满足并部署到
> kb.qh-cloud.com（实现文件为 `internal/application/service/user_sso.go`、
> `internal/handler/auth_sso.go`、`frontend/src/views/auth/SSOLogin.vue`，与本计划
> 预期的文件名不同但职责一致；测试见 `user_sso_test.go`）。下文「关键事实」第 1、2 条
> 已随之失效：SSO 代码在仓库内，2 分钟上限与地址栏清理均已上线。P1 及之后的任务待开工。

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 WeKnora 变成 SaaS 平台的受管知识库能力：SaaS 开通租户组即自动获得 WeKnora 工作空间；用户经 SaaS 单点登录进入对应空间；成员、角色与租户生命周期由 SaaS 实时事件驱动，SaaS 是身份与权限的唯一事实源。

**架构：** SaaS 侧维护可靠 outbox 事件表与投递 worker；WeKnora 侧新增外部租户绑定表与一组 platform-key 管理接口（幂等开通、成员同步、停用），SSO 交换接口从"令牌直接携带数字空间 ID"升级为"解析外部租户引用 → 绑定表 → 工作空间"。

**技术栈：** WeKnora Go/gin/gorm（现有）；SaaS Java + outbox 事件；传输 = REST + platform API Key（复用现有 `apiKeyPlatform` 网关，不新增认证机制）。

---

## 已锁定决策（Locked）

| 决策 | 结论 |
| --- | --- |
| 隔离边界 | 一个 SaaS 租户组 ↔ 一个 WeKnora 工作空间（tenant）；项目在空间内部用知识库/Agent/权限区分，不开空间 |
| 开通方式 | 事件驱动：SaaS 写 outbox → 投递 worker 幂等调 WeKnora；可重试、可对账；WeKnora 故障不阻塞 SaaS 主交易 |
| 权限时效 | 实时事件同步：成员新增、角色变更、移除、租户停用都立即在 WeKnora 生效，移除/降权须撤销会话 |
| SSO 基线 | 复用已上线的 JWT 短时令牌方案（`POST /api/v1/auth/sso/exchange`），不切换到 OIDC 作为第一优先 |

## 留给实现者裁量（Discretion）

- 绑定表命名与迁移编号（遵循 `migrations/` 现有风格）。
- 事件投递的退避策略与告警阈值。
- 令牌里 `role` 与空间角色的映射细节（沿用 viewer/contributor/admin，owner 拒绝）。

## 明确推迟（Deferred）

- 删除（而非停用）租户组时的数据清理策略。
- 项目级（空间内）KB 隔离与计费配额模型。
- SCIM、多 IdP、单点登出（SLO）。
- 每租户组的默认模型/存储后端自动预配（先由管理员配置一次）。

---

## 关键事实与风险（开工前必读）

1. ~~**SSO 代码不在本仓库。**~~（已解决：SSO 实现与测试在 `company/v0.8.3`，
   见 `internal/application/service/user_sso.go`。）服务器 `/opt/weknora/.env` 中的
   `WEKNORA_SSO_SECRET` 沿用，避免 SaaS 端换密钥。
2. ~~**已知的两个未部署加固项**~~（已解决：2 分钟上限随 `1b3a938` 部署，地址栏清理随
   前端镜像上线）。
3. **防重放缓存是进程内存**；多副本部署前必须换 Redis（本仓库已有 redis 依赖）。
4. 上一轮契约里 JWT 的 `tenant` 是 WeKnora 数字空间 ID，需要 SaaS 自己保存映射。本计划的 P1 把它改为外部租户引用，映射由 WeKnora 绑定表承担；过渡期两种 claim 都接受。

## SSO 契约（已验证，重落依据）

- 入口：`GET /sso?token=<JWT>`（前端）→ 读取后立刻从地址栏移除 token。
- 交换：`POST /api/v1/auth/sso/exchange`，body `{"token": "<JWT>"}`，返回与 `/auth/login` 相同的 `LoginResponse`。
- JWT（HS256，共享密钥 `WEKNORA_SSO_SECRET`）：

```json
{
  "iss": "qh-saas", "aud": "weknora",
  "sub": "SaaS 用户唯一ID",
  "email": "user@example.com", "name": "显示名",
  "tenant": "<外部租户引用，P1 起；旧部署为 WeKnora 数字空间 ID>",
  "role": "viewer | contributor | admin",   // owner 一律拒绝
  "jti": "一次性UUID",                       // 服务端重放校验
  "iat": 1792547525, "exp": 1792547585      // 服务端强制 exp-iat ≤ 120s
}
```

- 已验证行为：首登自动开户并按 `tenant`/`role` 加入空间；重放 401；空间不存在 403；无效/过期 401。
- 校验链：签名 → iss/aud → 过期与 2 分钟上限 → jti 重放 → 租户解析 → 用户 upsert → 成员关系 → 本地 JWT 对。

---

## 阶段与任务

### 任务 0（P0）：回收并重落 SSO 基线

**文件（预期落点，按现有分层）：**
- 创建：`internal/handler/sso.go`、`internal/application/service/sso.go`、`internal/types/sso.go`、`internal/utils/sso_jwt.go`
- 修改：`internal/router/routes_auth_tenant.go`（注册 `POST /auth/sso/exchange` 并加入免认证白名单）、`internal/config/config.go`（`WEKNORA_SSO_SECRET` 读取）、`docker-compose.yml` + `.env.example`（透传变量）、`frontend/src/router` + 登录视图（`/sso?token=` 路由与读取清除）
- 测试：`internal/utils/sso_jwt_test.go`（签名/过期/2 分钟上限/iss/aud/owner 拒绝）、`internal/handler/sso_test.go`（首登开户、重放 401、非法租户 403、disabled 403）

- [x] 步骤 1：写 `sso_jwt_test.go` 失败测试（签名、iss、aud、过期、2 分钟上限、role=owner 拒绝）
- [x] 步骤 2：运行确认失败：`go test ./internal/utils/ -run TestSSO -count=1`（undefined: ParseSSOClaims）
- [x] 步骤 3：实现 `internal/utils/sso_jwt.go`（HS256 解析与校验链，密钥与参数经 config 注入）
- [x] 步骤 4：测试转绿后 commit `feat(sso): add short-lived JWT verification chain`
- [x] 步骤 5：写 service/handler 层失败测试（开户、重放、角色同步、非法租户、disabled 账户、错误码映射）
- [x] 步骤 6：实现 `POST /auth/sso/exchange`（jti 重放：`ReplayCache` 接口 + 内存实现；Redis 实现随 P1 落地）+ `/sso` 前端路由
- [x] 步骤 7：`go test ./internal/application/service/ ./internal/handler/ -run 'TestLoginWithSSO|TestSSOExchange' -count=1` 转绿
- [x] 步骤 8：配置透传（compose/env.example/config.go），更新 `website-docs` 一节

> **环境说明（2026-10-23）**：本机 macOS 27 上 `go-m1cpu` 的 cgo 初始化会 SIGSEGV，导致
> `internal/application/service` 与 `internal/handler` 测试无法在本机运行；改用
> `docker run golang:1.26` 挂载源码与模块缓存执行，已全绿。`internal/handler` 的
> `TestPutTenantParserConfigAdminPreservesRedactedSecrets` 在 main 基线上同样失败，属既有问题。
> 前端依赖安装被 npm 的 EALLOWREMOTE 策略阻断，`npm run type-check` 需在依赖可用环境补跑。

### 任务 1（P1）：外部租户绑定与受管开通 API

**文件：**
- 创建：`internal/types/tenant_binding.go`、迁移（新增 `tenant_bindings` 表）、`internal/handler/managed_tenant.go`
- 修改：`internal/application/service/`（绑定的幂等创建/查询）、`internal/router/routes_auth_tenant.go` 或新建 `routes_managed.go`（platform-key 网关注册）、`internal/application/service/sso.go`（租户解析改为绑定表优先、数字 ID 兼容回退）
- 测试：绑定幂等、绑定停用后 SSO 403、platform key 越权 403、数字 ID 兼容

表草案（DDL 细节由实现者按迁移风格定稿）：

```sql
CREATE TABLE tenant_bindings (
  id BIGSERIAL PRIMARY KEY,
  external_source VARCHAR(64) NOT NULL DEFAULT 'qh-saas',
  external_tenant_id VARCHAR(128) NOT NULL,   -- SaaS 租户组稳定 ID
  tenant_id BIGINT NOT NULL REFERENCES tenants(id),
  status VARCHAR(16) NOT NULL DEFAULT 'active',  -- active | disabled
  created_at / updated_at / deleted_at
  -- UNIQUE (external_source, external_tenant_id)
);
```

API（全部走 `apiKeyPlatform(types.APIKeyCapabilitySystemTenantsManage)` 网关，幂等）：

| 端点 | 语义 |
| --- | --- |
| `PUT /system/admin/managed-tenants/:external_ref` | body `{name, active}`；不存在则建空间+绑定（幂等），存在则更新名称/状态；返回 `tenant_id` |
| `DELETE /system/admin/managed-tenants/:external_ref/members/:external_user` | 移除成员并撤销该用户全部会话（复用 `RevokeTokensByUserID`） |

成员同步端点见任务 2（与 SaaS 事件一一对应）。

- [x] 步骤 1：types + 迁移 + 绑定服务失败测试（幂等：同 external_ref 二次调用返回同 tenant_id）
- [x] 步骤 2：实现迁移与服务；测试转绿；commit
- [x] 步骤 3：handler + 路由注册（platform key 网关；鉴权由 API-key gate 与 SystemAdmin 中间件保证，服务层测试覆盖幂等/越权映射）
- [x] 步骤 4：实现 `PUT /system/admin/managed-tenants/:external_ref`；转绿；commit
- [x] 步骤 5：SSO 租户解析改绑定表优先；失败测试：绑定 disabled → 403；数字 `tenant` claim 旧路径回归测试；commit

### 任务 2（P2）：成员实时同步事件 + Redis 重放缓存

**文件：**
- 修改：`internal/handler/managed_tenant.go`（新增成员端点）、`internal/utils/` 重放缓存（Redis 实现，复用 `REDIS_ADDR`）
- 新成员端点：

| 端点 | 事件来源 | 语义 |
| --- | --- | --- |
| `PUT /system/admin/managed-tenants/:external_ref/members/:external_user` | `member.upserted` | body `{email, name, role, active}`；用户不存在则建（随机密码、`OidcOnlyLogin` 风格标记），存在则更新；成员关系 upsert；角色变更后撤销该用户会话（降权立即生效） |
| （同上 DELETE，P1 已落） | `member.removed` | 移除 + 撤销会话 |

**SaaS 侧（契约，另一仓库实现）：**
- outbox 表 `kb_provision_events(id, aggregate, event_type, payload JSON, status, retry_count, next_retry_at)`；event_type ∈ `tenant.provisioned` / `tenant.disabled` / `tenant.enabled` / `member.upserted` / `member.removed`。
- 投递 worker：按 `next_retry_at` 指数退避（1m/5m/30m/2h，上限 24h），连续失败告警；payload 附 `event_id` 供 WeKnora 幂等去重（实现者可在 WeKnora 侧按 external_ref+成员键天然幂等，event_id 仅做对账）。

- [ ] 步骤 1：成员端点失败测试（新建用户、角色变更撤销会话、移除撤销会话、owner 角色拒绝）
- [ ] 步骤 2：实现；转绿；commit `feat(managed): realtime member sync endpoints`
- [ ] 步骤 3：Redis 重放缓存失败测试（miniredis 已在 go.mod）
- [ ] 步骤 4：实现并按 `REDIS_ADDR` 有无自动降级内存实现；转绿；commit
- [ ] 步骤 5：SaaS 侧 outbox + worker 与 Java 签发示例（`WEKNORA_SSO_SECRET` 仅环境变量，禁止入库入仓）——在 SaaS 仓库单独排期，本文档仅冻结契约

### 任务 3（P3）：端到端验证与上线

- [ ] 步骤 1：本地 compose 起 postgres/redis/app/frontend，用测试脚本签发令牌走通：开通租户组 → SSO 登录 → 角色提升 → 移除 → 停用后登录 403
- [ ] 步骤 2：`make fmt && make lint && go test ./internal/... -count=1`（基线失败项如与本次改动无关，记录在 PR 描述）
- [ ] 步骤 3：部署前检查单：服务器 `.env` 沿用原 `WEKNORA_SSO_SECRET`；确认 2 分钟上限与地址栏清理已包含；关闭公开注册（系统设置 `auth.registration_mode=invite_register`，联系管理员确认 SaaS 侧已全部走 SSO）
- [ ] 步骤 4：清理上一轮测试数据：用户 `sso-test@qh-cloud.com`、空间 10002/10003/10004（先在服务器上核对再删）
- [ ] 步骤 5：PR + 部署 + 真实租户组灰度验证

---

## 自检记录

- 规格覆盖度：租户组→空间映射（任务 1）、事件驱动开通（任务 1+2）、实时成员/权限（任务 2）、SSO 进入（任务 0）、停用生效（任务 1/2/3 各有 disabled 分支）——均有对应任务。
- 占位符扫描：无"待定/后续实现"；SaaS Java outbox 属另一仓库，以契约冻结方式给出，不算本仓占位。
- 类型一致性：`external_ref`（路径参数）↔ `tenant_bindings.external_tenant_id`（列）；`external_user` ↔ 用户 upsert 键（优先 email，`sub` 作辅助）。角色取值统一 viewer/contributor/admin。
