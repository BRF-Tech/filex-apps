<script setup lang="ts">
// An app's price and license keys, for the members of its publisher and for
// administrators (the server decides who may; this page shows what it said).
// A new key is shown once, in the dialog that made it, and forgotten when
// the dialog closes: the store keeps only a fingerprint of it.
import { computed, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { KeyRound, Plus, TriangleAlert } from 'lucide-vue-next';
import {
  ApiError,
  createLicense,
  getApp,
  listActivations,
  listLicenses,
  releaseActivation,
  revokeLicense,
  setPricing,
  type Activation,
  type AppDetail,
  type License,
  type LicensePage,
  type Pricing,
} from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError, text, when, type Tone } from '@/lib/format';
import { setTitle } from '@/router';
import AccessGate from '@/components/AccessGate.vue';
import ActionDialog from '@/components/ActionDialog.vue';
import CopyField from '@/components/CopyField.vue';
import PageState from '@/components/PageState.vue';
import StatusBadge from '@/components/StatusBadge.vue';
import { session } from '@/lib/session';

const route = useRoute();
const name = computed(() => String(route.params.name ?? ''));
const app = ref<AppDetail | null>(null);
const loading = ref(true);
const error = ref('');
const notFound = ref(false);
const forbidden = ref(false);
const label = computed(() => (app.value ? text(app.value.label) || app.value.name : name.value));

const page = ref(1);
const PER_PAGE = 25;
const licences = ref<LicensePage | null>(null);
const listError = ref('');
const listLoading = ref(false);

async function load() {
  if (!session.me) return;
  loading.value = true;
  error.value = '';
  notFound.value = false;
  forbidden.value = false;
  try {
    const a = await getApp(name.value);
    app.value = a;
    setTitle(fmt(S.licensing.titleFor, { app: text(a.label) || a.name }));
    resetPricing(a.pricing);
    await loadLicences();
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound.value = true;
    else error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}

async function loadLicences() {
  listLoading.value = true;
  listError.value = '';
  try {
    licences.value = await listLicenses(name.value, { page: page.value, per_page: PER_PAGE });
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) forbidden.value = true;
    else listError.value = e instanceof Error ? e.message : String(e);
  } finally {
    listLoading.value = false;
  }
}
watch([name, () => session.me], () => void load(), { immediate: true });
watch(page, () => void loadLicences());

// ---------------------------------------------------------------- price
const model = ref<'free' | 'paid'>('free');
const priceText = ref('');
const purchaseUrl = ref('');
const pricingBusy = ref(false);
const pricingError = ref('');
const pricingDone = ref('');
function resetPricing(p?: Pricing) {
  model.value = p?.model ?? 'free';
  priceText.value = p?.price_text ?? '';
  purchaseUrl.value = p?.purchase_url ?? '';
}
const urlBad = computed(() => model.value === 'paid' && purchaseUrl.value.trim() !== '' && !/^https:\/\/\S+$/i.test(purchaseUrl.value.trim()));
async function savePricing() {
  pricingError.value = '';
  pricingDone.value = '';
  if (urlBad.value) return;
  pricingBusy.value = true;
  try {
    const body: Pricing =
      model.value === 'paid'
        ? { model: 'paid', price_text: priceText.value.trim() || undefined, purchase_url: purchaseUrl.value.trim() || undefined }
        : { model: 'free' };
    const saved = await setPricing(name.value, body);
    resetPricing(saved);
    pricingDone.value = S.pricing.saved;
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) forbidden.value = true;
    pricingError.value = e instanceof Error ? e.message : String(e);
  } finally {
    pricingBusy.value = false;
  }
}

// ---------------------------------------------------------------- licenses
const today = () => new Date().toISOString().slice(0, 10);
function stateOf(l: License): 'active' | 'revoked' | 'expired' {
  if (l.status === 'revoked') return 'revoked';
  return l.valid_until && l.valid_until < today() ? 'expired' : 'active';
}
const toneOfState = (s: 'active' | 'revoked' | 'expired'): Tone => (s === 'active' ? 'ok' : s === 'revoked' ? 'danger' : 'warn');

const columns = computed<DataColumn<License>[]>(() => [
  { id: 'key', label: S.licensing.colKey, width: 130, lead: true, format: (r) => r.key_prefix },
  { id: 'licensee', label: S.licensing.colLicensee, width: 220, format: (r) => [r.licensee_name, r.licensee_contact].filter(Boolean).join(' - ') },
  { id: 'seats', label: S.licensing.colSeats, width: 120, align: 'right', format: (r) => fmt(S.licensing.seatsOf, { used: r.seats_used, seats: r.seats }) },
  { id: 'valid', label: S.licensing.colValidUntil, width: 130, format: (r) => r.valid_until ?? S.licensing.noEnd },
  { id: 'updates', label: S.licensing.colUpdatesUntil, width: 130, format: (r) => r.updates_until ?? S.licensing.noEnd },
  { id: 'status', label: S.licensing.colStatus, width: 120, min: 100, format: (r) => S.licensing.status[stateOf(r)] },
  { id: 'created', label: S.licensing.colCreated, width: 150, format: (r) => when(r.created_at, false) },
]);
const actions = (r: License) => [
  { key: 'activations', label: S.licensing.showActivations },
  ...(r.status === 'active' ? [{ key: 'revoke', label: S.licensing.revoke, danger: true }] : []),
];

const selected = ref<License | null>(null);
const activations = ref<Activation[]>([]);
const actLoading = ref(false);
const actError = ref('');
const actDone = ref('');
async function showActivations(l: License) {
  selected.value = l;
  actLoading.value = true;
  actError.value = '';
  actDone.value = '';
  try {
    activations.value = (await listActivations(l.id)).items;
  } catch (e) {
    actError.value = e instanceof Error ? e.message : String(e);
  } finally {
    actLoading.value = false;
  }
}
const actColumns = computed<DataColumn<Activation>[]>(() => [
  { id: 'instance', label: S.licensing.colInstance, width: 240, lead: true, format: (r) => r.instance_id },
  { id: 'state', label: S.licensing.colState, width: 130, format: (r) => (r.active ? S.licensing.stateActive : S.licensing.stateReleased) },
  { id: 'first', label: S.licensing.colFirstSeen, width: 160, format: (r) => when(r.first_seen) },
  { id: 'last', label: S.licensing.colLastSeen, width: 160, format: (r) => when(r.last_seen) },
  { id: 'filex', label: S.licensing.colFilex, width: 180, format: (r) => r.filex_version ?? '' },
]);
const actActions = (r: Activation) => (r.active ? [{ key: 'release', label: S.licensing.release }] : []);
async function onActAction(key: string, r: Activation) {
  if (key !== 'release' || !selected.value) return;
  actError.value = '';
  try {
    await releaseActivation(selected.value.id, r.instance_id);
    actDone.value = S.licensing.released;
    await showActivations(selected.value);
    await loadLicences();
  } catch (e) {
    actError.value = e instanceof Error ? e.message : String(e);
  }
}

// create: a form, then the key once
const creating = ref(false);
const createBusy = ref(false);
const createError = ref('');
const createTried = ref(false);
const form = ref({ licensee_name: '', licensee_contact: '', seats: 1, valid_until: '', updates_until: '', note: '' });
const newKey = ref('');
function openCreate() {
  form.value = { licensee_name: '', licensee_contact: '', seats: 1, valid_until: '', updates_until: '', note: '' };
  createError.value = '';
  createTried.value = false;
  newKey.value = '';
  creating.value = true;
}
function closeCreate() {
  creating.value = false;
  newKey.value = '';
}
async function confirmCreate() {
  if (newKey.value) {
    closeCreate();
    return;
  }
  createTried.value = true;
  createError.value = '';
  const f = form.value;
  if (!f.licensee_name.trim() || !(f.seats >= 1)) return;
  createBusy.value = true;
  try {
    const r = await createLicense(name.value, {
      licensee_name: f.licensee_name.trim(),
      licensee_contact: f.licensee_contact.trim() || undefined,
      seats: Math.floor(f.seats),
      valid_until: f.valid_until || undefined,
      updates_until: f.updates_until || undefined,
      note: f.note.trim() || undefined,
    });
    newKey.value = r.key;
    await loadLicences();
  } catch (e) {
    createError.value = e instanceof Error ? e.message : String(e);
  } finally {
    createBusy.value = false;
  }
}

// revoke
const revoking = ref<License | null>(null);
const revokeBusy = ref(false);
const revokeError = ref('');
const done = ref('');
async function doRevoke() {
  if (!revoking.value) return;
  revokeBusy.value = true;
  revokeError.value = '';
  try {
    await revokeLicense(revoking.value.id);
    revoking.value = null;
    done.value = S.licensing.revoked;
    await loadLicences();
    if (selected.value) await showActivations(selected.value);
  } catch (e) {
    revokeError.value = e instanceof Error ? e.message : String(e);
  } finally {
    revokeBusy.value = false;
  }
}
function onAction(key: string, r: License) {
  done.value = '';
  if (key === 'activations') void showActivations(r);
  else if (key === 'revoke') {
    revokeError.value = '';
    revoking.value = r;
  }
}
const asLic = (r: unknown) => r as License;
const asAct = (r: unknown) => r as Activation;
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><KeyRound /></span>
      <div class="fa-pagehead__text">
        <h1 data-testid="manage-title">{{ fmt(S.licensing.titleFor, { app: label }) }}</h1>
        <p class="fa-pagehead__lead">{{ fmt(S.licensing.intro, { app: label }) }}</p>
      </div>
      <div class="fa-pagehead__actions">
        <RouterLink :to="{ name: 'app', params: { name } }" class="fe-btn">{{ S.common.open }}</RouterLink>
      </div>
    </div>
    <AccessGate need="github">
      <PageState :loading="loading && !app" :error="error" :not-found="notFound" @retry="load">
        <div v-if="forbidden" class="fa-callout fa-callout--warn fa-callout--row" role="alert" data-testid="manage-forbidden">
          <TriangleAlert class="fa-callout__icon" aria-hidden="true" />
          <div class="fa-callout__body"><p>{{ S.licensing.forbidden }}</p></div>
        </div>
        <template v-else-if="app">
          <section class="fa-panel" aria-labelledby="price-h" data-testid="pricing">
            <h2 id="price-h" class="fa-panel__title">{{ S.pricing.title }}</h2>
            <p class="fa-note">{{ S.pricing.intro }}</p>
            <form class="fa-form fe-form" novalidate data-testid="pricing-form" @submit.prevent="savePricing">
              <fieldset class="fa-fieldset">
                <legend>{{ S.pricing.model }}</legend>
                <div class="fa-checks">
                  <label class="fe-checkbox">
                    <input v-model="model" type="radio" name="fa-price-model" value="free" data-testid="pricing-free" />
                    <span>{{ S.pricing.modelFree }}</span>
                  </label>
                  <label class="fe-checkbox">
                    <input v-model="model" type="radio" name="fa-price-model" value="paid" data-testid="pricing-paid" />
                    <span>{{ S.pricing.modelPaid }}</span>
                  </label>
                </div>
              </fieldset>
              <template v-if="model === 'paid'">
                <div class="fe-field">
                  <label class="fe-field__label" for="fa-price-text">{{ S.pricing.priceText }}</label>
                  <input id="fa-price-text" v-model="priceText" class="fe-input" maxlength="80" aria-describedby="fa-price-text-h" data-testid="pricing-text" />
                  <p id="fa-price-text-h" class="fe-form__hint">{{ S.pricing.priceTextHint }}</p>
                </div>
                <div class="fe-field">
                  <label class="fe-field__label" for="fa-price-url">{{ S.pricing.purchaseUrl }}</label>
                  <input
                    id="fa-price-url"
                    v-model="purchaseUrl"
                    class="fe-input"
                    type="url"
                    inputmode="url"
                    maxlength="500"
                    aria-describedby="fa-price-url-h"
                    :aria-invalid="urlBad ? 'true' : undefined"
                    data-testid="pricing-url"
                  />
                  <p id="fa-price-url-h" class="fe-form__hint">{{ S.pricing.purchaseUrlHint }}</p>
                  <p v-if="urlBad" class="fe-form__error" role="alert">{{ S.pricing.urlInvalid }}</p>
                </div>
              </template>
              <div class="fa-actions">
                <button type="submit" class="fe-btn fe-btn--primary" :disabled="pricingBusy" data-testid="pricing-save">{{ S.pricing.save }}</button>
              </div>
              <p v-if="pricingDone" class="fa-note" role="status" data-testid="pricing-saved">{{ pricingDone }}</p>
              <p v-if="pricingError" class="fe-form__error" role="alert">{{ pricingError }}</p>
            </form>
          </section>

          <section class="fa-section" aria-labelledby="lic-h">
            <div class="fa-section__head">
              <h2 id="lic-h">{{ S.licensing.licences }}</h2>
              <div class="fa-actions">
                <button type="button" class="fe-btn fe-btn--primary" data-testid="licence-new" @click="openCreate">
                  <Plus class="fa-icon" aria-hidden="true" />{{ S.licensing.newLicence }}
                </button>
              </div>
            </div>
            <p v-if="done" class="fa-note" role="status">{{ done }}</p>
            <p v-if="listError" class="fa-callout fa-callout--danger" role="alert">{{ listError }}</p>
            <DataTable
              table-id="store.manage.licences"
              :columns="columns"
              :rows="licences?.items ?? []"
              :loading="listLoading"
              row-key="id"
              :empty="emptyUnlessError(S.licensing.empty, listError)"
              :aria-label="S.licensing.licences"
              :row-actions="actions"
              :row-actions-test-id="(r: License) => `licence-actions-${r.key_prefix}`"
              :page="page"
              :page-size="PER_PAGE"
              :total="licences?.total"
              data-testid="licences"
              @row-action="(key: string, row: License) => onAction(key, row)"
              @page="(p: number) => (page = p)"
            >
              <template #cell-key="{ row }">
                <div class="fa-cell"><code class="fa-code">{{ asLic(row).key_prefix }}…</code></div>
              </template>
              <template #cell-status="{ row }">
                <div class="fa-cell">
                  <StatusBadge :label="S.licensing.status[stateOf(asLic(row))]" :tone="toneOfState(stateOf(asLic(row)))" />
                </div>
              </template>
            </DataTable>
          </section>

          <section class="fa-section" aria-labelledby="act-h" data-testid="activations">
            <h2 id="act-h">{{ selected ? fmt(S.licensing.activationsFor, { key: selected.key_prefix + '…' }) : S.licensing.activations }}</h2>
            <p v-if="!selected" class="fa-note">{{ S.licensing.activationsHint }}</p>
            <template v-else>
              <p v-if="actDone" class="fa-note" role="status">{{ actDone }}</p>
              <p v-if="actError" class="fa-callout fa-callout--danger" role="alert">{{ actError }}</p>
              <DataTable
                table-id="store.manage.activations"
                :columns="actColumns"
                :rows="activations"
                :loading="actLoading"
                row-key="instance_id"
                :empty="emptyUnlessError(S.licensing.activationsEmpty, actError)"
                :aria-label="S.licensing.activations"
                :row-actions="actActions"
                data-testid="activations-table"
                @row-action="(key: string, row: Activation) => onActAction(key, row)"
              >
                <template #cell-instance="{ row }">
                  <div class="fa-cell"><code class="fa-code">{{ asAct(row).instance_id }}</code></div>
                </template>
              </DataTable>
            </template>
          </section>
        </template>
      </PageState>

      <ActionDialog
        :open="creating"
        :title="fmt(S.licensing.createTitle, { app: label })"
        :intro="newKey ? '' : S.licensing.createIntro"
        :confirm-label="newKey ? S.licensing.keyDone : S.licensing.create"
        :busy="createBusy"
        :error="createError"
        @confirm="confirmCreate"
        @cancel="closeCreate"
      >
        <div v-if="newKey" class="fa-stack" data-testid="licence-key">
          <p class="fa-callout__title">{{ S.licensing.keyTitle }}</p>
          <CopyField boxed :value="newKey" :label="`${S.common.copy}: ${S.licensing.keyLabel}`" testid="licence-key-value" />
          <p class="fa-callout fa-callout--warn fa-callout--row" role="note">
            <TriangleAlert class="fa-callout__icon" aria-hidden="true" />
            <span class="fa-callout__body">{{ S.licensing.keyWarning }}</span>
          </p>
        </div>
        <template v-else>
          <div class="fe-field">
            <label class="fe-field__label" for="fa-lic-name">{{ S.licensing.licensee }}</label>
            <input
              id="fa-lic-name"
              v-model="form.licensee_name"
              class="fe-input"
              maxlength="120"
              required
              aria-describedby="fa-lic-name-h"
              :aria-invalid="createTried && !form.licensee_name.trim() ? 'true' : undefined"
              data-testid="licence-licensee"
            />
            <p id="fa-lic-name-h" class="fe-form__hint">{{ S.licensing.licenseeHint }}</p>
            <p v-if="createTried && !form.licensee_name.trim()" class="fe-form__error" role="alert">{{ S.common.required }}</p>
          </div>
          <div class="fe-field">
            <label class="fe-field__label" for="fa-lic-contact">{{ S.licensing.contact }}</label>
            <input id="fa-lic-contact" v-model="form.licensee_contact" class="fe-input" maxlength="200" aria-describedby="fa-lic-contact-h" data-testid="licence-contact" />
            <p id="fa-lic-contact-h" class="fe-form__hint">{{ S.licensing.contactHint }}</p>
          </div>
          <div class="fe-field">
            <label class="fe-field__label" for="fa-lic-seats">{{ S.licensing.seats }}</label>
            <input id="fa-lic-seats" v-model.number="form.seats" class="fe-input" type="number" min="1" max="100000" step="1" aria-describedby="fa-lic-seats-h" data-testid="licence-seats" />
            <p id="fa-lic-seats-h" class="fe-form__hint">{{ S.licensing.seatsHint }}</p>
          </div>
          <div class="fe-field">
            <label class="fe-field__label" for="fa-lic-valid">{{ S.licensing.validUntil }}</label>
            <input id="fa-lic-valid" v-model="form.valid_until" class="fe-input" type="date" :min="today()" aria-describedby="fa-lic-valid-h" data-testid="licence-valid" />
            <p id="fa-lic-valid-h" class="fe-form__hint">{{ S.licensing.validUntilHint }}</p>
          </div>
          <div class="fe-field">
            <label class="fe-field__label" for="fa-lic-updates">{{ S.licensing.updatesUntil }}</label>
            <input id="fa-lic-updates" v-model="form.updates_until" class="fe-input" type="date" :min="today()" aria-describedby="fa-lic-updates-h" data-testid="licence-updates" />
            <p id="fa-lic-updates-h" class="fe-form__hint">{{ S.licensing.updatesUntilHint }}</p>
          </div>
          <div class="fe-field">
            <label class="fe-field__label" for="fa-lic-note">{{ S.licensing.note }}</label>
            <textarea id="fa-lic-note" v-model="form.note" class="fe-input" rows="2" maxlength="2000" aria-describedby="fa-lic-note-h" data-testid="licence-note"></textarea>
            <p id="fa-lic-note-h" class="fe-form__hint">{{ S.licensing.noteHint }}</p>
          </div>
        </template>
      </ActionDialog>

      <ActionDialog
        :open="!!revoking"
        :title="fmt(S.licensing.revokeTitle, { key: (revoking?.key_prefix ?? '') + '…' })"
        :intro="S.licensing.revokeIntro"
        :confirm-label="S.licensing.revokeConfirm"
        danger
        :busy="revokeBusy"
        :error="revokeError"
        @confirm="doRevoke"
        @cancel="revoking = null"
      />
    </AccessGate>
  </div>
</template>
