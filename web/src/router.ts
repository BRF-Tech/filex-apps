import { watch } from 'vue';
import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router';
import { S } from './strings';
import { brand } from './lib/brand';

// ⚠ Route NAMES and PATHS are part of the interface's contract with the
// e2e suite - change them only together with e2e/. Paths
// match the server's SPA fallback (everything that is not /api, /v1, /auth
// or /healthz).
export const routes: RouteRecordRaw[] = [
  { path: '/', name: 'catalog', component: () => import('./views/CatalogView.vue'), meta: { title: S.catalog.title } },
  { path: '/apps/:name', name: 'app', component: () => import('./views/AppView.vue'), meta: { title: '' } },
  { path: '/apps/:name/versions/:version', name: 'app-version', component: () => import('./views/AppView.vue'), meta: { title: '' } },
  { path: '/publishers', name: 'publishers', component: () => import('./views/PublishersView.vue'), meta: { title: S.publishers.title } },
  { path: '/publishers/:id', name: 'publisher', component: () => import('./views/PublisherView.vue'), meta: { title: '' } },
  { path: '/sign-in', name: 'sign-in', component: () => import('./views/SignInView.vue'), meta: { title: S.signIn.title } },
  { path: '/publish', name: 'publish', component: () => import('./views/PublishView.vue'), meta: { title: S.publish.title } },
  { path: '/publish/new-publisher', name: 'new-publisher', component: () => import('./views/NewPublisherView.vue'), meta: { title: S.newPublisher.title } },
  { path: '/publish/apps/:name', name: 'manage-app', component: () => import('./views/ManageAppView.vue'), meta: { title: S.licensing.title } },
  { path: '/me/instances', name: 'my-instances', component: () => import('./views/MyInstancesView.vue'), meta: { title: S.instances.title } },
  { path: '/me/notifications', name: 'my-notifications', component: () => import('./views/MyNotificationsView.vue'), meta: { title: S.notifications.title } },
  { path: '/submit', name: 'submit', component: () => import('./views/SubmitView.vue'), meta: { title: S.submit.title } },
  { path: '/submissions/:id(\\d+)', name: 'submission', component: () => import('./views/SubmissionView.vue'), meta: { title: '' } },
  { path: '/review', name: 'review', component: () => import('./views/ReviewQueueView.vue'), meta: { title: S.review.title } },
  {
    path: '/admin',
    component: () => import('./views/admin/AdminLayout.vue'),
    meta: { title: S.admin.title },
    children: [
      { path: '', name: 'admin', redirect: { name: 'admin-publishers' } },
      { path: 'publishers', name: 'admin-publishers', component: () => import('./views/admin/AdminPublishersView.vue'), meta: { title: S.admin.publishers } },
      { path: 'apps', name: 'admin-apps', component: () => import('./views/admin/AdminAppsView.vue'), meta: { title: S.admin.apps } },
      { path: 'index', name: 'admin-index', component: () => import('./views/admin/AdminIndexView.vue'), meta: { title: S.admin.index } },
      { path: 'validators', name: 'admin-validators', component: () => import('./views/admin/AdminValidatorsView.vue'), meta: { title: S.admin.validators } },
      { path: 'audit', name: 'admin-audit', component: () => import('./views/admin/AdminAuditView.vue'), meta: { title: S.admin.audit } },
    ],
  },
  { path: '/:rest(.*)*', name: 'not-found', component: () => import('./views/NotFoundView.vue'), meta: { title: S.common.notFoundTitle } },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior(to, from, saved) {
    if (saved) return saved;
    if (to.path === from.path) return false;
    return { top: 0 };
  },
});

let lastPage = '';

/** "Page - <the store's name>"; views with a name of their own (an app) call
 *  it again. The name is the server's (lib/brand.ts). */
export function setTitle(page: string): void {
  lastPage = page;
  document.title = page ? `${page} - ${brand.name}` : brand.name;
}

// The name can still change once the store answers (a dev server's page has
// no name of its own): the title follows.
watch(
  () => brand.name,
  () => setTitle(lastPage),
);

router.afterEach((to) => {
  const t = to.matched.reduce<string>((acc, r) => (typeof r.meta.title === 'string' && r.meta.title ? r.meta.title : acc), '');
  setTitle(t);
});
