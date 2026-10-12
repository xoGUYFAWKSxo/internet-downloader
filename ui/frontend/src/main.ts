import './style.css';
import {CheckEngine, CheckServer, ReceivedMedia} from '../wailsjs/go/main/App';

const statusEl = document.getElementById('engine-status')!;
const button = document.getElementById('recheck') as HTMLButtonElement;
const listEl = document.getElementById('media')!;
const emptyEl = document.getElementById('media-empty')!;
const LABEL: Record<string, string> = {hls: 'HLS', dash: 'DASH', video: 'Video', audio: 'Audio'};
const MODE: Record<string, string> = {
    multiplexed: 'parallel streams on one connection',
    ranges: 'several connections',
    single: 'one request',
};
// Server check results by item, kept here because the list redraws every few seconds.
const checks = new Map<number, {text: string, cls: string}>();

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
        const check = document.createElement('button');
        check.textContent = 'Check server';
        check.addEventListener('click', () => checkServer(m.id, check));
        const result = checks.get(m.id);
        li.append(title, el('div', 'url', m.url), el('div', 'meta', details.join(' · ')), check);
        if (result) li.append(el('div', 'probe ' + result.cls, result.text));
        return li;
    }));
}

function size(n: number) {
    if (n < 0) return 'size unknown';
    const units = ['bytes', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 ? n : n.toFixed(1)) + ' ' + units[i];
}

async function checkServer(id: number, btn: HTMLButtonElement) {
    btn.disabled = true;
    checks.set(id, {text: 'Checking the server…', cls: ''});
    await refreshMedia();
    try {
        const r = await CheckServer(id);
        const parts = [r.protocol, size(r.size), r.ranges ? 'accepts ranges' : 'no ranges'];
        let text = `${parts.join(' · ')}. Plan: ${MODE[r.plan.mode] ?? r.plan.mode}`;
        if (r.plan.mode !== 'single') text += ` (up to ${r.plan.streams})`;
        text += `. ${r.plan.reason}`;
        if (r.h3Error) text += ` HTTP/3 not used: ${r.h3Error}.`;
        checks.set(id, {text, cls: 'ok'});
    } catch (err) {
        checks.set(id, {text: `${err}`, cls: 'bad'});
    }
    await refreshMedia();
}

button.addEventListener('click', refresh);
refresh();
setInterval(refreshMedia, 3000);
