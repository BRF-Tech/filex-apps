import { createApp } from 'vue';
// filex's own stylesheet: the --fe-* design tokens (light and dark) and the
// one table's styles. The store draws with filex's palette, not a copy of it.
import '@brftech/filex-core/style.css';
import './styles.css';
import App from './App.vue';
import { router } from './router';
import { startTheme } from './lib/theme';
import { loadSession } from './lib/session';
import { loadBrand } from './lib/brand';

startTheme();
void loadSession();
void loadBrand();
createApp(App).use(router).mount('#app');
