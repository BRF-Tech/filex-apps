// The one way the SPA talks to the server. Same origin, cookie session; every
// mutating call carries the session's CSRF token (api/openapi.yaml
// "Conventions"). Types come from the contract: src/api/schema.d.ts is
// GENERATED from ../api/openapi.yaml by `pnpm gen:api` - edit the YAML, not
// the types.
//
// ⚠ The store's interface talks to the store and to nothing else
// (web/tests/guards.test.ts holds it to that): `api()` refuses any address that is
// not a path on this origin, so a GitHub address or a protocol-relative
// `//host` can never become a request from a viewer's browser. Links to GitHub
// are plain anchors the person chooses to follow.
import type { components } from './schema';

export type Schemas = components['schemas'];
export type AppSummary = Schemas['AppSummary'];
export type AppPage = Schemas['AppPage'];
export type AdminApp = Schemas['AdminApp'];
export type AdminAppPage = Schemas['AdminAppPage'];
export type AppStatus = AdminApp['status'];
export type AppDetail = Schemas['AppDetail'];
export type AppKind = Schemas['AppKind'];
export type VersionSummary = Schemas['VersionSummary'];
export type VersionDetail = Schemas['VersionDetail'];
export type PermissionRow = Schemas['PermissionRow'];
export type IndexArtifact = Schemas['IndexArtifact'];
export type BuildRow = Schemas['BuildRow'];
export type Conformance = Schemas['Conformance'];
export type IndexMedia = Schemas['IndexMedia'];
export type IndexRelease = Schemas['IndexRelease'];
export type Category = Schemas['Category'];
export type Publisher = Schemas['Publisher'];
export type PublisherRef = Schemas['PublisherRef'];
export type PublisherListItem = Schemas['PublisherListItem'];
export type PublisherPage = Schemas['PublisherPage'];
export type PublisherRequest = Schemas['PublisherRequest'];
export type Me = Schemas['Me'];
export type SignInProviders = Schemas['SignInProviders'];
export type StoreInfo = Schemas['StoreInfo'];
export type MePublisher = Me['publishers'][number];
export type Submission = Schemas['Submission'];
export type SubmissionStatus = Schemas['SubmissionStatus'];
export type SubmissionPage = Schemas['SubmissionPage'];
export type SubmissionDetail = Schemas['SubmissionDetail'];
export type SubmissionRequest = Schemas['SubmissionRequest'];
export type SubmissionKind = Schemas['SubmissionKind'];
export type Check = Schemas['Check'];
export type DryRunResult = Schemas['DryRunResult'];
export type Review = Schemas['Review'];
export type Resolution = Schemas['Resolution'];
export type ResolvedArtifact = Schemas['ResolvedArtifact'];
export type SigningKey = Schemas['SigningKey'];
export type ValidatorStatus = Schemas['ValidatorStatus'];
export type PluginValidatorStatus = Schemas['PluginValidatorStatus'];
export type AuditEntry = Schemas['AuditEntry'];
export type Withdrawal = Schemas['Withdrawal'];
export type Text = Schemas['Text'];
export type ApiErrorBody = Schemas['Error'];
export type Pricing = Schemas['Pricing'];
export type FileTypeUse = Schemas['FileTypeUse'];
export type FileTypeRole = Schemas['FileTypeRole'];
export type FileTypeSearch = Schemas['FileTypeSearch'];
export type BuiltinFileType = Schemas['BuiltinFileType'];
export type ConversionRow = Schemas['ConversionRow'];
export type License = Schemas['License'];
export type LicenseCreate = Schemas['LicenseCreate'];
export type LicenseCreated = Schemas['LicenseCreated'];
export type LicensePage = Schemas['LicensePage'];
export type Activation = Schemas['Activation'];
export type SavedInstance = Schemas['SavedInstance'];
export type SavedInstanceCreate = Schemas['SavedInstanceCreate'];
export type ConnectCode = Schemas['ConnectCode'];
export type InstallIntentRequest = Schemas['InstallIntentRequest'];
export type InstallIntentCreated = Schemas['InstallIntentCreated'];
export type NotificationKind = Schemas['NotificationKind'];
export type NotificationSettings = Schemas['NotificationSettings'];
export type Webhook = Schemas['Webhook'];
export type WebhookFormat = Schemas['WebhookFormat'];
export type WebhookCreate = Schemas['WebhookCreate'];
export type WebhookCreated = Schemas['WebhookCreated'];
export type WebhookPatch = Schemas['WebhookPatch'];
export type BrowserSubscription = Schemas['PushSubscription'];
export type BrowserSubscriptionCreate = Schemas['PushSubscriptionCreate'];
export type DeliveryResult = Schemas['DeliveryResult'];
export type ChannelResult = Schemas['ChannelResult'];

export type PublisherStatus = Publisher['status'];

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    /** The whole error body (an `Error` may carry more, e.g. the open submission). */
    readonly body: Record<string, unknown> = {},
    /** `Retry-After`, in seconds, on a 429. */
    readonly retryAfter: number | null = null,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

let csrfToken = '';

/** Called with /api/v1/me's csrf_token once a session exists ('' on sign-out). */
export function setCsrfToken(token: string): void {
  csrfToken = token;
}

/** A path on this store: one leading slash, never `//host` or `/\host`, never a scheme. */
export function isStorePath(path: string): boolean {
  return /^\/(?![/\\])/.test(path);
}

export type Query = Record<string, string | number | boolean | null | undefined>;

/** `?a=1&b=x`, skipping empty values; '' when nothing is left. */
export function queryString(q: Query = {}): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    p.set(k, String(v));
  }
  const s = p.toString();
  return s ? '?' + s : '';
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  if (!isStorePath(path)) throw new ApiError(0, 'not_a_store_path', 'Refused a request outside the store: ' + path);
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (method !== 'GET' && method !== 'HEAD' && csrfToken) headers['X-CSRF-Token'] = csrfToken;

  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (e) {
    throw new ApiError(0, 'network', e instanceof Error ? e.message : String(e));
  }
  const text = await res.text();
  const data: unknown = text ? safeJSON(text) : undefined;
  if (!res.ok) {
    const err = (data && typeof data === 'object' ? data : {}) as Record<string, unknown>;
    const ra = Number(res.headers.get('Retry-After'));
    throw new ApiError(
      res.status,
      typeof err.error === 'string' ? err.error : 'http_' + res.status,
      typeof err.message === 'string' ? err.message : res.statusText || 'HTTP ' + res.status,
      err,
      Number.isFinite(ra) && ra > 0 ? ra : null,
    );
  }
  return data as T;
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

const enc = encodeURIComponent;

// ---------------------------------------------------------------- catalog
export interface AppQuery extends Query {
  q?: string;
  kind?: AppKind | '';
  category?: string;
  publisher?: string;
  filex?: string;
  verified?: boolean;
  sort?: 'name' | 'updated' | 'published';
  page?: number;
  per_page?: number;
}
export const listApps = (q: AppQuery = {}) => api<AppPage>('GET', '/api/v1/apps' + queryString(q));
export const getApp = (name: string) => api<AppDetail>('GET', `/api/v1/apps/${enc(name)}`);
export const getAppVersion = (name: string, version: string) =>
  api<VersionDetail>('GET', `/api/v1/apps/${enc(name)}/versions/${enc(version)}`);
export interface PublisherQuery extends Query {
  q?: string;
  sort?: 'name' | 'apps';
  page?: number;
  per_page?: number;
}
export const listPublishers = (q: PublisherQuery = {}) => api<PublisherPage>('GET', '/api/v1/publishers' + queryString(q));
export const getPublisher = (id: string) =>
  api<{ publisher: Publisher; apps: AppSummary[] }>('GET', `/api/v1/publishers/${enc(id)}`);
export const listCategories = () => api<{ items: Category[] }>('GET', '/api/v1/categories');
export const getStore = () => api<StoreInfo>('GET', '/api/v1/store');

// ---------------------------------------------------------------- me / auth
export const getMe = () => api<Me>('GET', '/api/v1/me');
export const logout = () => api<void>('POST', '/auth/logout');

export const myApps = () => api<{ items: AdminApp[] }>('GET', '/api/v1/me/apps');

// ---------------------------------------------------------------- install links
// The magic link points at the person's own filex; the interface shows it and
// lets them open it, it never requests it.
export const createInstallIntent = (body: InstallIntentRequest) => api<InstallIntentCreated>('POST', '/api/v1/install-intents', body);
export const listInstances = () => api<{ items: SavedInstance[] }>('GET', '/api/v1/me/instances');
export const saveInstance = (body: SavedInstanceCreate) => api<SavedInstance>('POST', '/api/v1/me/instances', body);
export const deleteInstance = (id: string) => api<void>('DELETE', `/api/v1/me/instances/${enc(id)}`);
// Instance connections: a one-time code a filex administrator pastes into
// filex, and taking a connected filex's key away.
export const createConnectCode = (id: string) => api<ConnectCode>('POST', `/api/v1/me/instances/${enc(id)}/connect-code`);
export const disconnectInstance = (id: string) => api<void>('DELETE', `/api/v1/me/instances/${enc(id)}/connection`);

// ---------------------------------------------------------------- notifications
// The caller's own webhooks and browsers. The words of a notification are the
// server's; this side only chooses where and which events.
export const getNotifications = () => api<NotificationSettings>('GET', '/api/v1/me/notifications');
export const setPushEvents = (events: NotificationKind[]) =>
  api<{ events: NotificationKind[] }>('PUT', '/api/v1/me/notifications/push', { events });
export const subscribeBrowser = (body: BrowserSubscriptionCreate) => api<BrowserSubscription>('POST', '/api/v1/me/push-subscriptions', body);
export const unsubscribeBrowser = (id: string) => api<void>('DELETE', `/api/v1/me/push-subscriptions/${enc(id)}`);
export const testBrowser = (id: string) => api<DeliveryResult>('POST', `/api/v1/me/push-subscriptions/${enc(id)}/test`);
export const createWebhook = (body: WebhookCreate) => api<WebhookCreated>('POST', '/api/v1/me/webhooks', body);
export const updateWebhook = (id: string, body: WebhookPatch) => api<Webhook>('PATCH', `/api/v1/me/webhooks/${enc(id)}`, body);
export const deleteWebhook = (id: string) => api<void>('DELETE', `/api/v1/me/webhooks/${enc(id)}`);
export const testWebhook = (id: string) => api<DeliveryResult>('POST', `/api/v1/me/webhooks/${enc(id)}/test`);

// ---------------------------------------------------------------- licensing
export const setPricing = (name: string, body: Pricing) => api<Pricing>('PUT', `/api/v1/apps/${enc(name)}/pricing`, body);
export const listLicenses = (name: string, q: { page?: number; per_page?: number } = {}) =>
  api<LicensePage>('GET', `/api/v1/apps/${enc(name)}/licenses` + queryString(q));
export const createLicense = (name: string, body: LicenseCreate) => api<LicenseCreated>('POST', `/api/v1/apps/${enc(name)}/licenses`, body);
export const revokeLicense = (id: string, reason = '') =>
  api<License>('POST', `/api/v1/licenses/${enc(id)}/revoke`, reason ? { reason } : {});
export const listActivations = (id: string) => api<{ items: Activation[] }>('GET', `/api/v1/licenses/${enc(id)}/activations`);
export const releaseActivation = (id: string, instanceId: string) =>
  api<Activation>('POST', `/api/v1/licenses/${enc(id)}/activations/${enc(instanceId)}/release`);

// ---------------------------------------------------------------- publishing
export const requestPublisher = (body: PublisherRequest) => api<Publisher>('POST', '/api/v1/publishers', body);
export const createSubmission = (body: SubmissionRequest) => api<Submission>('POST', '/api/v1/submissions', body);
export const listSubmissions = (q: { status?: SubmissionStatus | ''; publisher?: string; page?: number; per_page?: number } = {}) =>
  api<SubmissionPage>('GET', '/api/v1/submissions' + queryString(q));
export const getSubmission = (id: number) => api<SubmissionDetail>('GET', `/api/v1/submissions/${id}`);
export const withdrawSubmission = (id: number) => api<Submission>('POST', `/api/v1/submissions/${id}/withdraw`);
export const retrySubmission = (id: number) => api<Submission>('POST', `/api/v1/submissions/${id}/retry`);

// ---------------------------------------------------------------- review
export const reviewQueue = (q: { page?: number; per_page?: number } = {}) =>
  api<SubmissionPage>('GET', '/api/v1/review/queue' + queryString(q));
export const approveSubmission = (id: number, body: { note?: string; security?: boolean }) =>
  api<{ submission: Submission; version: VersionDetail; index_serial: number }>('POST', `/api/v1/review/submissions/${id}/approve`, body);
export const rejectSubmission = (id: number, reason: string) =>
  api<Submission>('POST', `/api/v1/review/submissions/${id}/reject`, { reason });
export const commentSubmission = (id: number, reason: string) =>
  api<Review>('POST', `/api/v1/review/submissions/${id}/comment`, { reason });
export const recheckSubmission = (id: number) => api<Submission>('POST', `/api/v1/review/submissions/${id}/recheck`);

// ---------------------------------------------------------------- admin
export const adminUpdateApp = (name: string, body: { status?: 'listed' | 'unlisted'; categories?: string[] }) =>
  api<AppDetail>('PATCH', `/api/v1/admin/apps/${enc(name)}`, body);
export const revokeApp = (name: string, reason: string) =>
  api<AppDetail>('POST', `/api/v1/admin/apps/${enc(name)}/revoke`, { reason });
export const unrevokeApp = (name: string) => api<AppDetail>('DELETE', `/api/v1/admin/apps/${enc(name)}/revoke`);
export const yankVersion = (name: string, version: string, reason: string) =>
  api<VersionDetail>('POST', `/api/v1/admin/apps/${enc(name)}/versions/${enc(version)}/yank`, { reason });
export const unyankVersion = (name: string, version: string) =>
  api<VersionDetail>('DELETE', `/api/v1/admin/apps/${enc(name)}/versions/${enc(version)}/yank`);
export interface AdminAppQuery extends Query {
  status?: AppStatus | 'all';
  q?: string;
  sort?: 'name' | 'updated';
  page?: number;
  per_page?: number;
}
export const adminListApps = (q: AdminAppQuery = {}) => api<AdminAppPage>('GET', '/api/v1/admin/apps' + queryString(q));
export const adminListPublishers = (q: { status?: PublisherStatus | ''; page?: number; per_page?: number } = {}) =>
  api<{ items: Publisher[]; total: number }>('GET', '/api/v1/admin/publishers' + queryString(q));
export const adminUpdatePublisher = (
  id: string,
  body: { status?: 'active' | 'suspended'; status_reason?: string; verified?: boolean; official?: boolean },
) => api<Publisher>('PATCH', `/api/v1/admin/publishers/${enc(id)}`, body);
export const publishIndex = () => api<IndexRelease>('POST', '/api/v1/admin/index/publish');
export const adminListKeys = () => api<{ items: SigningKey[] }>('GET', '/api/v1/admin/keys');
export const adminListValidators = () =>
  api<{ items: ValidatorStatus[]; plugin_validators?: PluginValidatorStatus[] }>('GET', '/api/v1/admin/validators');
export const adminAudit = (q: { action?: string; target_type?: string; target_id?: string; page?: number; per_page?: number } = {}) =>
  api<{ items: AuditEntry[]; total: number; chain_ok: boolean }>('GET', '/api/v1/admin/audit' + queryString(q));

/** The latest index's serial and sha256, from the headers of a HEAD (no body read). */
export async function indexHead(): Promise<{ serial: number | null; etag: string | null; ready: boolean }> {
  const res = await fetch('/v1/index.json', { method: 'HEAD', credentials: 'same-origin' });
  if (!res.ok) return { serial: null, etag: null, ready: false };
  const s = Number(res.headers.get('X-Index-Serial'));
  const etag = (res.headers.get('ETag') ?? '').replace(/^W\//, '').replace(/"/g, '') || null;
  return { serial: Number.isFinite(s) && s > 0 ? s : null, etag, ready: true };
}
