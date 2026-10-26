<script setup lang="ts">
// A signed-in person's notifications: this browser (Web Push) and webhooks,
// and which events each one hears. The server writes every notification and
// sends it; this page only chooses where and which events. An
// administrator is also offered the administrators' events (the server asks
// GitHub again whenever one goes out).
import { computed, onMounted, ref, watch } from 'vue';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { Bell } from 'lucide-vue-next';
import {
  ApiError,
  createWebhook,
  deleteWebhook,
  getNotifications,
  setPushEvents,
  subscribeBrowser,
  testBrowser,
  testWebhook,
  unsubscribeBrowser,
  updateWebhook,
  type BrowserSubscription,
  type ChannelResult,
  type DeliveryResult,
  type NotificationKind,
  type NotificationSettings,
  type Webhook,
  type WebhookFormat,
} from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError, when } from '@/lib/format';
import { session } from '@/lib/session';
import {
  PushRefused,
  currentSubscription,
  pushPermission,
  pushSupported,
  rememberId,
  rememberedId,
  subscribeThisBrowser,
  unsubscribeThisBrowser,
  type PushState,
} from '@/lib/push';
import AccessGate from '@/components/AccessGate.vue';
import ActionDialog from '@/components/ActionDialog.vue';
import CopyField from '@/components/CopyField.vue';
import FaSegmented from '@/components/FaSegmented.vue';

const settings = ref<NotificationSettings | null>(null);
const loading = ref(false);
const error = ref('');
const status = ref('');
const statusFailed = ref(false);

function message(e: unknown): string {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

async function load() {
  if (!session.me) return;
  loading.value = true;
  error.value = '';
  try {
    settings.value = await getNotifications();
    pushEvents.value = [...settings.value.push.events];
    await readBrowser();
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: message(e) });
  } finally {
    loading.value = false;
  }
}
watch(() => session.me, () => void load(), { immediate: true });

const kinds = computed(() => settings.value?.events ?? []);
const userKinds = computed(() => kinds.value.filter((k) => !k.admin).map((k) => k.kind));
const adminKinds = computed(() => kinds.value.filter((k) => k.admin).map((k) => k.kind));
const allKinds = computed(() => kinds.value.map((k) => k.kind));
const label = (k: NotificationKind) => S.notifications.events[k] ?? k;

function lastText(last: ChannelResult | undefined): string {
  if (!last) return S.notifications.never;
  const word = last.status === 'delivered' ? S.notifications.delivered : S.notifications.failed;
  const parts = [fmt(S.notifications.lastLine, { status: word, when: when(last.at) })];
  if (last.http_status) parts.push(fmt(S.notifications.lastHttp, { code: last.http_status }));
  if (last.status === 'failed' && last.error) parts.push(last.error);
  return parts.join(' · ');
}

function say(text: string, failed = false) {
  status.value = text;
  statusFailed.value = failed;
}

function resultText(name: string, r: DeliveryResult): string {
  return r.status === 'delivered'
    ? fmt(S.notifications.testDelivered, { name, status: r.http_status ?? '' })
    : fmt(S.notifications.testFailed, { name, error: r.error || (r.http_status ? fmt(S.notifications.lastHttp, { code: r.http_status }) : S.notifications.failed) });
}

// ----------------------------------------------------------------- this browser
const permission = ref<PushState>('unsupported');
const thisId = ref('');
const pushBusy = ref(false);
const pushError = ref('');
const pushEvents = ref<NotificationKind[]>([]);
const pushSaved = ref(false);

const browsers = computed(() => settings.value?.push.browsers ?? []);
const browserOn = computed(() => !!thisId.value && browsers.value.some((b) => b.id === thisId.value));

async function readBrowser() {
  permission.value = pushPermission();
  thisId.value = '';
  if (!pushSupported()) return;
  try {
    const sub = await currentSubscription();
    const id = rememberedId();
    if (sub && id && browsers.value.some((b) => b.id === id)) thisId.value = id;
  } catch {
    // A browser that refuses to answer is treated as off.
  }
}
onMounted(() => {
  permission.value = pushPermission();
});

async function turnOn() {
  const key = settings.value?.push.public_key;
  if (!key) return;
  pushBusy.value = true;
  pushError.value = '';
  try {
    const sub = await subscribeThisBrowser(key);
    const row = await subscribeBrowser({
      endpoint: sub.endpoint ?? '',
      expirationTime: sub.expirationTime ?? null,
      keys: { p256dh: sub.keys?.p256dh ?? '', auth: sub.keys?.auth ?? '' },
    });
    rememberId(row.id);
    await load();
    say(S.notifications.browserOn);
  } catch (e) {
    permission.value = pushPermission();
    pushError.value = e instanceof PushRefused ? (e.state === 'denied' ? S.notifications.browserDenied : S.notifications.browserOff) : message(e);
  } finally {
    pushBusy.value = false;
  }
}

async function turnOff() {
  pushBusy.value = true;
  pushError.value = '';
  try {
    if (thisId.value) await unsubscribeBrowser(thisId.value).catch((e) => (e instanceof ApiError && e.status === 404 ? undefined : Promise.reject(e)));
    await unsubscribeThisBrowser();
    rememberId('');
    await load();
    say(S.notifications.browserOff);
  } catch (e) {
    pushError.value = message(e);
  } finally {
    pushBusy.value = false;
  }
}

async function testThisBrowser(id = thisId.value) {
  if (!id) return;
  try {
    const r = await testBrowser(id);
    say(resultText(S.notifications.thisBrowser, r), r.status !== 'delivered');
    await load();
  } catch (e) {
    say(message(e), true);
  }
}

async function togglePushEvent(kind: NotificationKind, on: boolean) {
  const next = on ? [...pushEvents.value, kind] : pushEvents.value.filter((k) => k !== kind);
  pushEvents.value = allKinds.value.filter((k) => next.includes(k));
  pushSaved.value = false;
  pushError.value = '';
  try {
    pushEvents.value = (await setPushEvents(pushEvents.value)).events;
    pushSaved.value = true;
  } catch (e) {
    pushError.value = message(e);
  }
}

function browserName(b: BrowserSubscription): string {
  const ua = b.user_agent || '';
  const m = /(Edg|Firefox|Chrome|Safari|OPR)\/[\d.]+/.exec(ua);
  const os = /(Windows|Android|iPhone|iPad|Mac OS X|Linux)/.exec(ua);
  const name = m ? m[1].replace('Edg', 'Edge').replace('OPR', 'Opera') : S.common.unknown;
  return os ? `${name}, ${os[1].replace('Mac OS X', 'macOS')}` : name;
}
const browserColumns = computed<DataColumn<BrowserSubscription>[]>(() => [
  { id: 'browser', label: S.notifications.colBrowser, width: 220, lead: true, format: (r) => (r.id === thisId.value ? `${browserName(r)} (${S.notifications.thisBrowser})` : browserName(r)) },
  { id: 'service', label: S.notifications.colService, width: 220, format: (r) => r.endpoint_host },
  { id: 'since', label: S.notifications.colSince, width: 150, format: (r) => when(r.created_at, false) },
  { id: 'last', label: S.notifications.colLast, width: 320, format: (r) => lastText(r.last) },
]);
const browserActions = () => [
  { key: 'test', label: S.notifications.sendTest },
  { key: 'remove', label: S.notifications.removeBrowser, danger: true },
];
const removingBrowser = ref<BrowserSubscription | null>(null);
const removeBrowserBusy = ref(false);
const removeBrowserError = ref('');
function onBrowserAction(key: string, row: BrowserSubscription) {
  if (key === 'test') void testThisBrowser(row.id);
  else if (key === 'remove') {
    removeBrowserError.value = '';
    removingBrowser.value = row;
  }
}
async function doRemoveBrowser() {
  const row = removingBrowser.value;
  if (!row) return;
  removeBrowserBusy.value = true;
  try {
    if (row.id === thisId.value) {
      await turnOff();
    } else {
      await unsubscribeBrowser(row.id);
      await load();
    }
    removingBrowser.value = null;
  } catch (e) {
    removeBrowserError.value = message(e);
  } finally {
    removeBrowserBusy.value = false;
  }
}

// ----------------------------------------------------------------- webhooks
const webhooks = computed(() => settings.value?.webhooks ?? []);
const formatOptions = computed(() =>
  (settings.value?.formats ?? ['generic', 'slack', 'discord', 'teams']).map((f) => ({ value: f as WebhookFormat, label: S.notifications.formats[f] })),
);

const columns = computed<DataColumn<Webhook>[]>(() => [
  { id: 'name', label: S.notifications.colName, width: 200, lead: true },
  { id: 'format', label: S.notifications.colFormat, width: 140, format: (r) => S.notifications.formats[r.format] + (r.signed ? ` (${S.notifications.signed})` : '') },
  { id: 'address', label: S.notifications.colAddress, width: 260, format: (r) => r.url_hint },
  {
    id: 'events',
    label: S.notifications.colEvents,
    width: 150,
    format: (r) => (r.events.length ? fmt(S.notifications.eventsCount, { n: r.events.length, total: allKinds.value.length }) : S.notifications.noEvents),
  },
  { id: 'last', label: S.notifications.colLast, width: 320, format: (r) => lastText(r.last) },
]);
const hookActions = () => [
  { key: 'test', label: S.notifications.test },
  { key: 'edit', label: S.notifications.edit },
  { key: 'remove', label: S.notifications.remove, danger: true },
];
function onHookAction(key: string, row: Webhook) {
  if (key === 'test') void runTest(row);
  else if (key === 'edit') startEdit(row);
  else if (key === 'remove') {
    removeError.value = '';
    removing.value = row;
  }
}

async function runTest(row: Webhook) {
  say(S.notifications.testing);
  try {
    const r = await testWebhook(row.id);
    say(resultText(row.name, r), r.status !== 'delivered');
    await load();
  } catch (e) {
    say(message(e), true);
  }
}

// Add a webhook
const adding = ref(false);
const addBusy = ref(false);
const addError = ref('');
const tried = ref(false);
const form = ref({ name: '', format: 'generic' as WebhookFormat, url: '', headerName: '', headerValue: '', events: [] as NotificationKind[] });
const urlBad = computed(() => !/^https?:\/\/[^\s/]+/i.test(form.value.url.trim()));
const headerBad = computed(() => form.value.format === 'generic' && !form.value.headerName.trim() !== !form.value.headerValue);
const eventsBad = computed(() => form.value.events.length === 0);
function startAdd() {
  form.value = { name: '', format: 'generic', url: '', headerName: '', headerValue: '', events: [...allKinds.value] };
  tried.value = false;
  addError.value = '';
  adding.value = true;
}
async function doAdd() {
  tried.value = true;
  addError.value = '';
  if (!form.value.name.trim() || urlBad.value || headerBad.value || eventsBad.value) return;
  addBusy.value = true;
  try {
    const generic = form.value.format === 'generic';
    const out = await createWebhook({
      name: form.value.name.trim(),
      format: form.value.format,
      url: form.value.url.trim(),
      header_name: generic && form.value.headerName.trim() ? form.value.headerName.trim() : undefined,
      header_value: generic && form.value.headerName.trim() ? form.value.headerValue : undefined,
      events: form.value.events,
    });
    adding.value = false;
    secret.value = out.secret ?? '';
    await load();
    say(S.notifications.added);
  } catch (e) {
    addError.value = message(e);
  } finally {
    addBusy.value = false;
  }
}
const secret = ref('');

// Edit a webhook's name and events
const editing = ref<Webhook | null>(null);
const editName = ref('');
const editEvents = ref<NotificationKind[]>([]);
const editBusy = ref(false);
const editError = ref('');
function startEdit(row: Webhook) {
  editing.value = row;
  editName.value = row.name;
  editEvents.value = [...row.events];
  editError.value = '';
}
async function doEdit() {
  const row = editing.value;
  if (!row) return;
  if (!editName.value.trim()) {
    editError.value = S.common.required;
    return;
  }
  editBusy.value = true;
  try {
    await updateWebhook(row.id, { name: editName.value.trim(), events: allKinds.value.filter((k) => editEvents.value.includes(k)) });
    editing.value = null;
    await load();
  } catch (e) {
    editError.value = message(e);
  } finally {
    editBusy.value = false;
  }
}

// Delete a webhook
const removing = ref<Webhook | null>(null);
const removeBusy = ref(false);
const removeError = ref('');
async function doRemove() {
  const row = removing.value;
  if (!row) return;
  removeBusy.value = true;
  try {
    await deleteWebhook(row.id);
    removing.value = null;
    await load();
  } catch (e) {
    removeError.value = message(e);
  } finally {
    removeBusy.value = false;
  }
}
const asHook = (r: unknown) => r as Webhook;
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><Bell /></span>
      <div class="fa-pagehead__text">
        <h1>{{ S.notifications.title }}</h1>
        <p class="fa-pagehead__lead">{{ S.notifications.intro }}</p>
      </div>
    </div>
    <AccessGate need="github">
      <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
      <p v-if="status" class="fa-callout" :class="statusFailed ? 'fa-callout--danger' : 'fa-callout--ok'" role="status" data-testid="notify-status">
        {{ status }}
      </p>

      <section v-if="settings" class="fa-panel" aria-labelledby="nt-push-h" data-testid="notify-browser">
        <h2 id="nt-push-h" class="fa-panel__title">{{ S.notifications.browserTitle }}</h2>
        <p class="fa-note">{{ S.notifications.browserIntro }}</p>
        <p v-if="!settings.push.available" class="fa-note" data-testid="push-unavailable">{{ S.notifications.browserUnavailable }}</p>
        <p v-else-if="permission === 'unsupported'" class="fa-note" data-testid="push-unsupported">{{ S.notifications.browserUnsupported }}</p>
        <template v-else>
          <p v-if="permission === 'denied'" class="fa-callout fa-callout--warn" data-testid="push-denied">{{ S.notifications.browserDenied }}</p>
          <p class="fa-note" data-testid="push-state">{{ browserOn ? S.notifications.browserOn : S.notifications.browserOff }}</p>
          <div class="fa-actions">
            <template v-if="browserOn">
              <button type="button" class="fe-btn" :disabled="pushBusy" data-testid="push-test" @click="testThisBrowser()">{{ S.notifications.sendTest }}</button>
              <button type="button" class="fe-btn" :disabled="pushBusy" data-testid="push-off" @click="turnOff">{{ S.notifications.turnOff }}</button>
            </template>
            <button v-else type="button" class="fe-btn fe-btn--primary" :disabled="pushBusy || permission === 'denied'" data-testid="push-on" @click="turnOn">
              {{ S.notifications.turnOn }}
            </button>
          </div>
          <p v-if="pushError" class="fe-form__error" role="alert" data-testid="push-error">{{ pushError }}</p>
          <fieldset class="fa-fieldset" data-testid="push-events">
            <legend>{{ S.notifications.browserEvents }}</legend>
            <p v-if="adminKinds.length" class="fa-note">{{ S.notifications.forYou }}</p>
            <div class="fa-checks">
              <label v-for="k in userKinds" :key="k" class="fe-checkbox">
                <input type="checkbox" :checked="pushEvents.includes(k)" :data-testid="`push-event-${k}`" @change="togglePushEvent(k, ($event.target as HTMLInputElement).checked)" />
                <span>{{ label(k) }}</span>
              </label>
            </div>
            <template v-if="adminKinds.length">
              <p class="fa-note">{{ S.notifications.forAdmins }}</p>
              <div class="fa-checks">
                <label v-for="k in adminKinds" :key="k" class="fe-checkbox">
                  <input type="checkbox" :checked="pushEvents.includes(k)" :data-testid="`push-event-${k}`" @change="togglePushEvent(k, ($event.target as HTMLInputElement).checked)" />
                  <span>{{ label(k) }}</span>
                </label>
              </div>
            </template>
            <p v-if="pushSaved" class="fa-note" role="status">{{ S.notifications.browserEventsSaved }}</p>
          </fieldset>
        </template>
        <h3 id="nt-browsers-h">{{ S.notifications.browsers }}</h3>
        <DataTable
          table-id="store.me.browsers"
          :columns="browserColumns"
          :rows="browsers"
          :loading="loading"
          row-key="id"
          :empty="emptyUnlessError(S.notifications.browsersEmpty, error)"
          :aria-label="S.notifications.browsers"
          :row-actions="browserActions"
          data-testid="browsers"
          :row-actions-test-id="(r: BrowserSubscription) => `browser-actions-${r.id}`"
          @row-action="onBrowserAction"
        />
      </section>

      <section v-if="settings" class="fa-section" aria-labelledby="nt-hooks-h" data-testid="notify-webhooks">
        <h2 id="nt-hooks-h">{{ S.notifications.webhooksTitle }}</h2>
        <p class="fa-note">{{ S.notifications.webhooksIntro }}</p>
        <div class="fa-actions">
          <button type="button" class="fe-btn fe-btn--primary" data-testid="webhook-add" @click="startAdd">{{ S.notifications.addWebhook }}</button>
        </div>
        <DataTable
          table-id="store.me.webhooks"
          :columns="columns"
          :rows="webhooks"
          :loading="loading"
          row-key="id"
          :empty="emptyUnlessError(S.notifications.webhooksEmpty, error)"
          :aria-label="S.notifications.webhooksTitle"
          :row-actions="hookActions"
          data-testid="webhooks"
          :row-actions-test-id="(r: Webhook) => `webhook-actions-${r.id}`"
          @row-action="onHookAction"
        >
          <template #cell-address="{ row }">
            <div class="fa-cell"><code class="fa-code">{{ asHook(row).url_hint }}</code></div>
          </template>
        </DataTable>
      </section>

      <ActionDialog
        :open="adding"
        :title="S.notifications.addTitle"
        :confirm-label="S.notifications.add"
        :busy="addBusy"
        :error="addError"
        @confirm="doAdd"
        @cancel="adding = false"
      >
        <div class="fe-field">
          <label class="fe-field__label" for="nt-name">{{ S.notifications.name }}</label>
          <input id="nt-name" v-model="form.name" class="fe-input" maxlength="80" aria-describedby="nt-name-h" data-testid="webhook-name" />
          <p id="nt-name-h" class="fe-form__hint">{{ S.notifications.nameHint }}</p>
          <p v-if="tried && !form.name.trim()" class="fe-form__error" role="alert">{{ S.common.required }}</p>
        </div>
        <div class="fe-field">
          <span id="nt-format-l" class="fe-field__label">{{ S.notifications.format }}</span>
          <FaSegmented v-model="form.format" :options="formatOptions" aria-labelledby="nt-format-l" testid="webhook-format" />
          <p class="fe-form__hint" data-testid="webhook-format-hint">{{ S.notifications.formatHints[form.format] }}</p>
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="nt-url">{{ S.notifications.url }}</label>
          <input
            id="nt-url"
            v-model="form.url"
            class="fe-input"
            type="url"
            inputmode="url"
            maxlength="2048"
            spellcheck="false"
            autocomplete="off"
            placeholder="https://"
            aria-describedby="nt-url-h"
            :aria-invalid="tried && urlBad ? 'true' : undefined"
            data-testid="webhook-url"
          />
          <p id="nt-url-h" class="fe-form__hint">{{ S.notifications.urlHint }}</p>
          <p v-if="tried && urlBad" class="fe-form__error" role="alert">{{ S.notifications.urlInvalid }}</p>
        </div>
        <template v-if="form.format === 'generic'">
          <div class="fe-field">
            <label class="fe-field__label" for="nt-hname">{{ S.notifications.headerName }}</label>
            <input id="nt-hname" v-model="form.headerName" class="fe-input" maxlength="64" spellcheck="false" autocomplete="off" placeholder="Authorization" aria-describedby="nt-hname-h" data-testid="webhook-header-name" />
            <p id="nt-hname-h" class="fe-form__hint">{{ S.notifications.headerNameHint }}</p>
          </div>
          <div class="fe-field">
            <label class="fe-field__label" for="nt-hvalue">{{ S.notifications.headerValue }}</label>
            <input id="nt-hvalue" v-model="form.headerValue" class="fe-input" type="password" maxlength="1024" autocomplete="off" aria-describedby="nt-hvalue-h" data-testid="webhook-header-value" />
            <p id="nt-hvalue-h" class="fe-form__hint">{{ S.notifications.headerValueHint }}</p>
            <p v-if="tried && headerBad" class="fe-form__error" role="alert">{{ S.notifications.headerBoth }}</p>
          </div>
        </template>
        <fieldset class="fa-fieldset" data-testid="webhook-events">
          <legend>{{ S.notifications.chooseEvents }}</legend>
          <p v-if="adminKinds.length" class="fa-note">{{ S.notifications.forYou }}</p>
          <div class="fa-checks">
            <label v-for="k in userKinds" :key="k" class="fe-checkbox">
              <input v-model="form.events" type="checkbox" :value="k" :data-testid="`webhook-event-${k}`" />
              <span>{{ label(k) }}</span>
            </label>
          </div>
          <template v-if="adminKinds.length">
            <p class="fa-note">{{ S.notifications.forAdmins }}</p>
            <div class="fa-checks">
              <label v-for="k in adminKinds" :key="k" class="fe-checkbox">
                <input v-model="form.events" type="checkbox" :value="k" :data-testid="`webhook-event-${k}`" />
                <span>{{ label(k) }}</span>
              </label>
            </div>
          </template>
          <p v-if="tried && eventsBad" class="fe-form__error" role="alert">{{ S.notifications.pickOne }}</p>
        </fieldset>
      </ActionDialog>

      <ActionDialog
        :open="!!secret"
        :title="S.notifications.secretTitle"
        :intro="S.notifications.secretIntro"
        :confirm-label="S.notifications.secretDone"
        @confirm="secret = ''"
        @cancel="secret = ''"
      >
        <div v-if="secret" class="fe-field" data-testid="webhook-secret">
          <span class="fe-field__label">{{ S.notifications.secretLabel }}</span>
          <CopyField :value="secret" :label="S.notifications.secretLabel" testid="webhook-secret-value" boxed />
        </div>
      </ActionDialog>

      <ActionDialog
        :open="!!editing"
        :title="fmt(S.notifications.editTitle, { name: editing?.name ?? '' })"
        :confirm-label="S.notifications.editSave"
        :busy="editBusy"
        :error="editError"
        @confirm="doEdit"
        @cancel="editing = null"
      >
        <div class="fe-field">
          <label class="fe-field__label" for="nt-ename">{{ S.notifications.name }}</label>
          <input id="nt-ename" v-model="editName" class="fe-input" maxlength="80" data-testid="webhook-edit-name" />
        </div>
        <fieldset class="fa-fieldset" data-testid="webhook-edit-events">
          <legend>{{ S.notifications.chooseEvents }}</legend>
          <div class="fa-checks">
            <label v-for="k in allKinds" :key="k" class="fe-checkbox">
              <input v-model="editEvents" type="checkbox" :value="k" :data-testid="`webhook-edit-event-${k}`" />
              <span>{{ label(k) }}</span>
            </label>
          </div>
        </fieldset>
      </ActionDialog>

      <ActionDialog
        :open="!!removing"
        :title="fmt(S.notifications.removeTitle, { name: removing?.name ?? '' })"
        :intro="S.notifications.removeIntro"
        :confirm-label="S.notifications.removeConfirm"
        danger
        :busy="removeBusy"
        :error="removeError"
        @confirm="doRemove"
        @cancel="removing = null"
      />

      <ActionDialog
        :open="!!removingBrowser"
        :title="S.notifications.removeBrowserTitle"
        :intro="S.notifications.removeBrowserIntro"
        :confirm-label="S.notifications.removeBrowserConfirm"
        danger
        :busy="removeBrowserBusy"
        :error="removeBrowserError"
        @confirm="doRemoveBrowser"
        @cancel="removingBrowser = null"
      />
    </AccessGate>
  </div>
</template>
