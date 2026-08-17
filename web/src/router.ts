import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/', redirect: '/map' },
  { path: '/map', component: () => import('./views/MapView.vue') },
  { path: '/feeds', component: () => import('./views/FeedsView.vue') },
  { path: '/cities', redirect: '/feeds' }, // pre-rename links
  { path: '/build', component: () => import('./views/BuildView.vue') },
  { path: '/service', component: () => import('./views/ServiceView.vue') },
  { path: '/sketch', component: () => import('./views/SketchView.vue') },
  { path: '/tuning', component: () => import('./views/TuningView.vue') },
  { path: '/style', component: () => import('./views/StyleView.vue') },
  { path: '/areas', component: () => import('./views/AreasView.vue') },
]

export default createRouter({
  history: createWebHistory('/console/'),
  routes,
})
