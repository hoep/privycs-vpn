import { createRouter, createWebHashHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    redirect: '/connection',
  },
  {
    path: '/connection',
    name: 'Connection',
    component: () => import('@/views/ConnectionView.vue'),
  },
  {
    path: '/add',
    name: 'AddConnection',
    component: () => import('@/views/AddConnectionView.vue'),
  },
  {
    path: '/protocols',
    name: 'Protocols',
    component: () => import('@/views/ProtocolSelector.vue'),
  },
  {
    path: '/connections',
    name: 'Connections',
    component: () => import('@/views/ConnectionsView.vue'),
  },
  {
    path: '/settings',
    name: 'Settings',
    component: () => import('@/views/SettingsView.vue'),
  },
  {
    path: '/logs',
    name: 'Logs',
    component: () => import('@/views/LogsView.vue'),
  },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

export default router
