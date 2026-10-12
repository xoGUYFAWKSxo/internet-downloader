import './style.css';
import {CancelDownload, CheckEngine, CheckServer, Download, Downloads, ReceivedMedia, ShowInFolder} from '../wailsjs/go/main/App';

const statusEl = document.getElementById('engine-status')!;
const button = document.getElementById('recheck') as HTMLButtonElement;
const listEl = document.getElementById('media')!;
const emptyEl = document.getElementById('media-empty')!;
const dlEl = document.getElementById('downloads')!;
const dlEmptyEl = document.getElementById('downloads-empty')!;
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
        const dl = document.createElement('button');
        dl.textContent = 'Download';
        li.append(' ', dl);
        if (m.kind === 'video' || m.kind === 'audio') {
            dl.addEventListener('click', () => startDownload(m.id, dl));
        } else {
            // Say why on the item itself, not only in a note under the list.
            dl.disabled = true;
            li.append(el('div', 'probe', `${LABEL[m.kind] ?? m.kind} streams can't be downloaded yet; that comes in the next version. Plain video and audio files can.`));
        }
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

async function startDownload(id: number, btn: HTMLButtonElement) {
    btn.disabled = true;
    try {
        await Download(id);
        checks.delete(id);
    } catch (err) {
        checks.set(id, {text: `${err}`, cls: 'bad'});
        await refreshMedia();
    }
    btn.disabled = false;
    await refreshDownloads();
}

const STATE: Record<string, string> = {
    checking: 'Checking the server…', downloading: 'Downloading', done: 'Done', failed: 'Failed', cancelled: 'Cancelled',
};
// The last sample per download, to work out the speed from two real readings.
const samples = new Map<number, {done: number, at: number, speed: number}>();

function speedOf(id: number, done: number): number | null {
    const now = performance.now();
    const prev = samples.get(id);
    if (!prev) {
        samples.set(id, {done, at: now, speed: -1});
        return null;
    }
    if (now - prev.at < 900) return prev.speed < 0 ? null : prev.speed;
    const speed = (done - prev.done) / ((now - prev.at) / 1000);
    samples.set(id, {done, at: now, speed});
    return speed;
}

async function refreshDownloads() {
    let list;
    try {
        list = (await Downloads()) ?? [];
    } catch {
        return;
    }
    dlEmptyEl.hidden = list.length > 0;
    dlEl.replaceChildren(...list.map((d) => {
        const li = document.createElement('li');
        const active = d.state === 'checking' || d.state === 'downloading';
        const cls = d.state === 'done' ? 'ok' : d.state === 'failed' ? 'bad' : '';
        const title = el('div', 'title', '');
        title.append(el('span', 'kind ' + cls, STATE[d.state] ?? d.state), el('span', '', d.name));
        li.append(title);
        if (d.state !== 'checking') {
            const parts = [d.size >= 0 ? `${size(d.done)} of ${size(d.size)}` : size(d.done)];
            if (d.size > 0) parts.push(`${Math.floor(d.done * 100 / d.size)}%`);
            if (d.state === 'downloading') {
                const s = speedOf(d.id, d.done);
                if (s !== null) parts.push(`${size(Math.round(s))}/s`);
            }
            if (d.protocol) parts.push(`${d.protocol}, ${d.streams} ${d.streams === 1 ? 'request' : 'parallel requests'}`);
            li.append(el('div', 'meta', parts.join(' · ')));
            if (d.size > 0) {
                const bar = document.createElement('progress');
                bar.max = d.size;
                bar.value = d.done;
                li.append(bar);
            }
        }
        if (d.error) li.append(el('div', 'probe bad', d.error));
        if (active) {
            const cancel = document.createElement('button');
            cancel.textContent = 'Cancel';
            cancel.addEventListener('click', async () => {
                cancel.disabled = true;
                try { await CancelDownload(d.id); } catch { /* the list shows the outcome */ }
                await refreshDownloads();
            });
            li.append(cancel);
        }
        if (d.state === 'done' && d.path) {
            const show = document.createElement('button');
            show.textContent = 'Show in folder';
            show.addEventListener('click', () => ShowInFolder(d.path!).catch((err) => li.append(el('div', 'probe bad', `${err}`))));
            li.append(show);
        }
        return li;
    }));
}

button.addEventListener('click', refresh);
refresh();
refreshDownloads();
setInterval(refreshMedia, 3000);
setInterval(refreshDownloads, 1000);
