import './style.css';
import {CheckEngine, ReceivedMedia} from '../wailsjs/go/main/App';

const statusEl = document.getElementById('engine-status')!;
const button = document.getElementById('recheck') as HTMLButtonElement;
const listEl = document.getElementById('media')!;
const emptyEl = document.getElementById('media-empty')!;
const LABEL: Record<string, string> = {hls: 'HLS', dash: 'DASH', video: 'Video', audio: 'Audio'};

async function refresh() {
    button.disabled = true;
    statusEl.className = '';
    statusEl.textContent = 'Checking the download engine…';
    try {
        const s = await CheckEngine();
        statusEl.className = s.running ? 'ok' : 'bad';
        statusEl.textContent = s.running ? `${s.message} Version ${s.version}.` : s.message;
    } catch (err) {
        statusEl.className = 'bad';
        statusEl.textContent = `Couldn't check the engine: ${err}`;
    } finally {
        button.disabled = false;
    }
    await refreshMedia();
}

function el(tag: string, cls: string, text: string) {
    const e = document.createElement(tag);
    e.className = cls;
    e.textContent = text;
    return e;
}

async function refreshMedia() {
    let items;
    try {
        items = (await ReceivedMedia()) ?? [];
    } catch {
        return; // The engine status line already says what's wrong.
    }
    emptyEl.hidden = items.length > 0;
    listEl.replaceChildren(...items.map((m) => {
        const li = document.createElement('li');
        const title = el('div', 'title', '');
        title.append(el('span', 'kind', LABEL[m.kind] ?? m.kind), el('span', '', m.pageTitle || new URL(m.url).host));
        const details = [`from ${m.browser}`, new Date(m.received).toLocaleTimeString(), `${m.cookies} cookies`];
        if (m.manifestBytes) details.push(`playlist ${m.manifestBytes} bytes`);
        li.append(title, el('div', 'url', m.url), el('div', 'meta', details.join(' · ')));
        return li;
    }));
}

button.addEventListener('click', refresh);
refresh();
setInterval(refreshMedia, 3000);
