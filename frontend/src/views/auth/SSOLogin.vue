<template>
  <div class="sso-page">
    <div class="sso-card">
      <h2>知识库</h2>
      <template v-if="state === 'loading'">
        <p class="sso-text">正在登录…</p>
      </template>
      <template v-else-if="state === 'error'">
        <p class="sso-text sso-error">{{ errorMessage }}</p>
        <a class="sso-link" href="/login">返回登录</a>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { ssoExchange, userInfoFromApi } from '@/api/auth'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()

const state = ref<'loading' | 'error'>('loading')
const errorMessage = ref('单点登录失败，请从原系统重新进入。')

const persistLoginResponse = (response: any) => {
  const activeTenant = response.active_tenant || response.tenant
  if (!response.user || !response.token) return false

  const homeTenantIdRaw = response.user.tenant_id ?? activeTenant?.id ?? ''
  authStore.setUser(userInfoFromApi(response.user, homeTenantIdRaw))
  authStore.setToken(response.token)
  if (response.refresh_token) {
    authStore.setRefreshToken(response.refresh_token)
  }
  if (activeTenant) {
    authStore.setTenant({
      id: String(activeTenant.id) || '',
      name: activeTenant.name || '',
      owner_id: response.user.id || '',
      created_at: activeTenant.created_at || new Date().toISOString(),
      updated_at: activeTenant.updated_at || new Date().toISOString()
    })
  } else {
    authStore.setTenant(null)
  }
  if (Array.isArray(response.memberships)) {
    authStore.setMemberships(response.memberships)
  }
  const activeIdNum = Number(activeTenant?.id)
  const homeIdNum = Number(homeTenantIdRaw)
  if (Number.isFinite(activeIdNum) && Number.isFinite(homeIdNum) && activeIdNum !== homeIdNum) {
    authStore.setSelectedTenant(activeIdNum, activeTenant?.name || null)
  } else {
    authStore.setSelectedTenant(null, null)
  }
  return true
}

onMounted(async () => {
  const token = String(route.query.token || '')
  if (!token) {
    state.value = 'error'
    errorMessage.value = '缺少登录令牌，请从原系统重新进入。'
    return
  }
  const response = await ssoExchange(token)
  if (response.success && persistLoginResponse(response)) {
    await router.replace('/platform/knowledge-bases')
    return
  }
  state.value = 'error'
  errorMessage.value = response.message || '登录令牌无效或已过期，请从原系统重新进入。'
})
</script>

<style scoped>
.sso-page {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  background: #f5f7fa;
}

.sso-card {
  width: 360px;
  padding: 40px 32px;
  text-align: center;
  background: #fff;
  border-radius: 12px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.08);
}

.sso-card h2 {
  margin: 0 0 16px;
  font-size: 20px;
  color: #1f2329;
}

.sso-text {
  margin: 0;
  color: #646a73;
  font-size: 14px;
}

.sso-error {
  color: #d54941;
  line-height: 1.6;
}

.sso-link {
  display: inline-block;
  margin-top: 16px;
  color: #0052d9;
  font-size: 14px;
  text-decoration: none;
}
</style>
