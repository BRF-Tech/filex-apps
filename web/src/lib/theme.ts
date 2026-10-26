// Light or dark. The palette is filex's own (`@brftech/filex-core/style.css`):
// its dark values are published on `:root.dark` (variables.css), so the store
// follows a choice by toggling that one class on <html>. The choice is a
// per-browser convenience (localStorage, guarded: a private window or blocked
// storage simply follows the system).
import { effectScope, ref, watch, type Ref } from 'vue';

export type ThemePref = 'system' | 'light' | 'dark';
export const THEME_KEY = 'fapps.theme';
const PREFS: ThemePref[] = ['system', 'light', 'dark'];

function readPref(): ThemePref {
  try {
    const v = localStorage.getItem(THEME_KEY) as ThemePref | null;
    return v && PREFS.includes(v) ? v : 'system';
  } catch {
    return 'system';
  }
}

function systemDark(): boolean {
  try {
    return typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: dark)').matches;
  } catch {
    return false;
  }
}

export function resolveDark(pref: ThemePref, sysDark: boolean): boolean {
  return pref === 'dark' || (pref === 'system' && sysDark);
}

export const themePref: Ref<ThemePref> = ref(readPref());
const sysDark = ref(systemDark());
/** What the tables are told (their column and Actions menus teleport to <body>). */
export const tableTheme: Ref<'light' | 'dark'> = ref(resolveDark(themePref.value, sysDark.value) ? 'dark' : 'light');

export function applyTheme(root: HTMLElement = document.documentElement): void {
  const dark = resolveDark(themePref.value, sysDark.value);
  root.classList.toggle('dark', dark);
  root.style.colorScheme = dark ? 'dark' : 'light';
  tableTheme.value = dark ? 'dark' : 'light';
}

export function setThemePref(pref: ThemePref): void {
  themePref.value = PREFS.includes(pref) ? pref : 'system';
  try {
    if (themePref.value === 'system') localStorage.removeItem(THEME_KEY);
    else localStorage.setItem(THEME_KEY, themePref.value);
  } catch {
    /* storage blocked: the choice lasts for this page */
  }
}

let started = false;

/** Apply now and follow both the choice and the system from here on. */
export function startTheme(): void {
  if (started) return;
  started = true;
  applyTheme();
  // A detached scope: the watcher lives as long as the page, not as long as
  // whichever component happened to start it.
  effectScope(true).run(() => watch([themePref, sysDark], () => applyTheme()));
  try {
    matchMedia('(prefers-color-scheme: dark)').addEventListener('change', (e) => {
      sysDark.value = e.matches;
    });
  } catch {
    /* no matchMedia: stays on the first answer */
  }
}
