import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import FaSelect from '@/components/FaSelect.vue';
import FaSegmented from '@/components/FaSegmented.vue';
import { settle } from './helpers';

const OPTIONS = [
  { value: '', label: 'All' },
  { value: 'queued', label: 'Queued' },
  { value: 'resolving', label: 'Resolving' },
  { value: 'rejected', label: 'Rejected', disabled: true },
  { value: 'needs_review', label: 'Needs review' },
  { value: 'withdrawn', label: 'Withdrawn' },
];

function host(opts: { required?: boolean; disabled?: boolean; start?: string; inDialog?: boolean } = {}) {
  const value = ref(opts.start ?? '');
  const changes: string[] = [];
  const C = defineComponent({
    setup() {
      return () => {
        const field = h('div', [
          h('label', { for: 'st' }, 'Status'),
          h(FaSelect, {
            id: 'st',
            modelValue: value.value,
            options: OPTIONS,
            required: opts.required,
            disabled: opts.disabled,
            requiredMessage: 'Required.',
            testid: 'st',
            'onUpdate:modelValue': (v: string) => (value.value = v),
            onChange: (v: string) => changes.push(v),
          }),
          h('button', { id: 'after', type: 'button' }, 'after'),
        ]);
        const form = h('form', { id: 'f' }, [field]);
        return opts.inDialog ? h('dialog', { open: true }, [form]) : form;
      };
    },
  });
  const w = mount(C, { attachTo: document.body });
  return { w, value, changes, combo: () => w.find('[role="combobox"]') };
}
const list = () => document.querySelector('[role="listbox"]');
const opts = () => Array.from(document.querySelectorAll<HTMLElement>('[role="option"]'));

afterEach(() => {
  document.body.replaceChildren();
});

describe('FaSelect', () => {
  it('is a labelled combobox (no native select), the label names and opens it', async () => {
    const { w, combo } = host();
    expect(w.find('select').exists()).toBe(false);
    const c = combo();
    expect(c.element.tagName).toBe('BUTTON');
    expect(c.attributes('id')).toBe('st');
    expect(document.querySelector('label[for="st"]')).not.toBeNull();
    expect(c.attributes('aria-haspopup')).toBe('listbox');
    expect(c.attributes('aria-expanded')).toBe('false');
    expect(c.text()).toContain('All');
    await c.trigger('click');
    await settle();
    expect(c.attributes('aria-expanded')).toBe('true');
    expect(list()).not.toBeNull();
    expect(c.attributes('aria-controls')).toBe(list()!.id);
    expect(opts().map((o) => o.textContent?.trim())).toEqual(['All', 'Queued', 'Resolving', 'Rejected', 'Needs review', 'Withdrawn']);
    expect(opts()[0].getAttribute('aria-selected')).toBe('true');
    expect(c.attributes('aria-activedescendant')).toBe(opts()[0].id);
  });

  it('moves with the arrow keys (skipping disabled options), Home and End, and chooses with Enter', async () => {
    const { value, changes, combo } = host();
    const c = combo();
    await c.trigger('keydown', { key: 'ArrowDown' });
    await settle();
    expect(list()).not.toBeNull();
    await c.trigger('keydown', { key: 'ArrowDown' });
    await c.trigger('keydown', { key: 'ArrowDown' });
    await c.trigger('keydown', { key: 'ArrowDown' });
    await settle();
    expect(c.attributes('aria-activedescendant')).toBe(opts()[4].id);
    await c.trigger('keydown', { key: 'End' });
    await settle();
    expect(c.attributes('aria-activedescendant')).toBe(opts()[5].id);
    await c.trigger('keydown', { key: 'Home' });
    await c.trigger('keydown', { key: 'ArrowDown' });
    await c.trigger('keydown', { key: 'Enter' });
    await settle();
    expect(value.value).toBe('queued');
    expect(changes).toEqual(['queued']);
    expect(list()).toBeNull();
    expect(c.attributes('aria-expanded')).toBe('false');
  });

  it('Escape closes without a change; Tab chooses the active option and closes', async () => {
    const { value, combo } = host({ start: 'queued' });
    const c = combo();
    await c.trigger('keydown', { key: 'ArrowDown' });
    await c.trigger('keydown', { key: 'ArrowDown' });
    await c.trigger('keydown', { key: 'Escape' });
    await settle();
    expect(value.value).toBe('queued');
    expect(list()).toBeNull();
    await c.trigger('keydown', { key: 'ArrowDown' });
    await c.trigger('keydown', { key: 'ArrowDown' });
    await c.trigger('keydown', { key: 'Tab' });
    await settle();
    expect(value.value).toBe('resolving');
    expect(list()).toBeNull();
  });

  it('typing jumps to the next matching option, open or closed', async () => {
    const { value, combo } = host();
    const c = combo();
    await c.trigger('keydown', { key: 'w' });
    await settle();
    expect(c.attributes('aria-activedescendant')).toBe(opts()[5].id);
    await c.trigger('keydown', { key: 'Enter' });
    await settle();
    expect(value.value).toBe('withdrawn');
    await c.trigger('keydown', { key: 'n' });
    await c.trigger('keydown', { key: 'e' });
    await settle();
    expect(c.attributes('aria-activedescendant')).toBe(opts()[4].id);
  });

  it('a click on an option chooses it; a press outside closes it', async () => {
    const { value, combo } = host();
    await combo().trigger('click');
    await settle();
    opts()[2].click();
    await settle();
    expect(value.value).toBe('resolving');
    expect(list()).toBeNull();
    await combo().trigger('click');
    await settle();
    document.getElementById('after')!.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    await settle();
    expect(list()).toBeNull();
    expect(value.value).toBe('resolving');
    opts();
  });

  it('a disabled option and a disabled select cannot be chosen', async () => {
    const { value, combo } = host();
    await combo().trigger('click');
    await settle();
    opts()[3].click();
    await settle();
    expect(value.value).toBe('');
    const off = host({ disabled: true });
    expect(off.combo().attributes('disabled')).toBeDefined();
    await off.combo().trigger('keydown', { key: 'ArrowDown' });
    await settle();
    expect(document.querySelectorAll('[role="listbox"]').length).toBe(1);
  });

  it('required: an empty value makes the form invalid and is marked; a value makes it valid', async () => {
    const { w, value, combo } = host({ required: true });
    await settle();
    const form = w.find('form').element as HTMLFormElement;
    expect(combo().attributes('aria-required')).toBe('true');
    expect(form.checkValidity()).toBe(false);
    await settle();
    expect(combo().attributes('aria-invalid')).toBe('true');
    value.value = 'queued';
    await settle();
    expect(form.checkValidity()).toBe(true);
    expect(combo().attributes('aria-invalid')).toBeUndefined();
  });

  it('inside an open dialog the panel goes into that dialog (a modal dialog sits above the page)', async () => {
    const { w, combo } = host({ inDialog: true });
    await combo().trigger('click');
    await settle();
    const dialog = w.find('dialog').element;
    expect(list()?.parentElement).toBe(dialog);
  });
});

describe('FaSegmented', () => {
  it('is a radio group: the chosen button is the only tab stop and the arrow keys move the choice', async () => {
    const value = ref('');
    const C = defineComponent({
      setup: () => () =>
        h(FaSegmented, {
          modelValue: value.value,
          options: [
            { value: '', label: 'All' },
            { value: 'listed', label: 'Listed' },
            { value: 'revoked', label: 'Revoked' },
          ],
          ariaLabel: 'Status',
          testid: 'seg',
          'onUpdate:modelValue': (v: string) => (value.value = v),
        }),
    });
    const w = mount(C, { attachTo: document.body });
    const g = w.find('[data-testid="seg"]');
    expect(g.attributes('role')).toBe('radiogroup');
    const radios = () => g.findAll('[role="radio"]');
    expect(radios().map((r) => r.attributes('data-testid'))).toEqual(['seg-all', 'seg-listed', 'seg-revoked']);
    expect(radios().map((r) => r.attributes('aria-checked'))).toEqual(['true', 'false', 'false']);
    expect(radios().map((r) => r.attributes('tabindex'))).toEqual(['0', '-1', '-1']);
    await radios()[0].trigger('keydown', { key: 'ArrowLeft' });
    await settle();
    expect(value.value).toBe('revoked');
    await w.find('[data-testid="seg-listed"]').trigger('click');
    await settle();
    expect(radios().map((r) => r.attributes('aria-checked'))).toEqual(['false', 'true', 'false']);
  });
});
