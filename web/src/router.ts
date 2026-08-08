import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  { path: '/', redirect: '/map' },
  { path: '/map', component: () => import('./views/MapView.vue') },
  { path: '/cities', component: () => import('./views/CitiesView.vue') },
  { path: '/build', component: () => import('./views/BuildView.vue') },
  { path: '/service', component: () => import('./views/ServiceView.vue') },
  { path: '/style', component: () => import('./views/StyleView.vue') },
  { path: '/areas', component: () => import('./views/AreasView.vue') },
]

export default createRouter({
  history: createWebHistory('/console/'),
  routes,
})
