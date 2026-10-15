import {AddToQueue, AddURL, CheckEngine, CheckServer, Download, Downloads, MoveInQueue, OpenFile, PauseDownload, QueueInfo, ReceivedMedia, RemoveDownloads, RemoveFromQueue, ResumeDownload, ShowInFolder, ShowOptions, ShowProgress, StartQueue, StopQueue} from '../wailsjs/go/main/App';
import {ClipboardGetText} from '../wailsjs/runtime/runtime';
import {$, duration, el, msg, percent, size} from './util';

type Status = Awaited<ReturnType<typeof Downloads>>[number];

// Window settings such as the sort order are kept per user; nothing breaks
// when the browser storage isn't there.
function stored<T>(key: string, fallback: T): T {
    try {
        const v = localStorage.getItem(key);
        return v ? JSON.parse(v) as T : fallback;
    } catch {
        return fallback;
    }
}
function store(key: string, v: unknown) {
    try { localStorage.setItem(key, JSON.stringify(v)); } catch { /* not kept */ }
}

// --- File types, by extension, for the category list ---

const TYPES: Record<string, {label: string, ext: string[]}> = {
    compressed: {label: 'Compressed', ext: ['zip', 'rar', '7z', 'tar', 'gz', 'tgz', 'bz2', 'xz', 'zst', 'iso', 'cab']},
    documents: {label: 'Documents', ext: ['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'odp', 'rtf', 'txt', 'csv', 'epub', 'md']},
    music: {label: 'Music', ext: ['mp3', 'm4a', 'aac', 'flac', 'wav', 'ogg', 'opus', 'wma']},
    programs: {label: 'Programs', ext: ['exe', 'msi', 'msix', 'appx', 'appxbundle', 'bat', 'cmd', 'ps1', 'apk', 'dmg', 'deb', 'rpm']},
    video: {label: 'Video', ext: ['mp4', 'mkv', 'webm', 'avi', 'mov', 'wmv', 'flv', 'm4v', 'ts', 'mpg', 'mpeg', '3gp']},
};

function typeOf(name: string) {
    const dot = name.lastIndexOf('.');
    const ext = dot >= 0 ? name.slice(dot + 1).toLowerCase() : '';
    for (const [t, {ext: list}] of Object.entries(TYPES)) if (list.includes(ext)) return t;
    return '';
}

const RUNNING = new Set(['checking', 'downloading', 'joining', 'waiting']);
const isRunning = (d: Status) => RUNNING.has(d.state);

// --- Categories ---

const ICON = {
    folder: '<svg viewBox="0 0 16 16"><path d="M2 4h4.5L8 5.5h6V13H2z"/></svg>',
    unfinished: '<svg viewBox="0 0 16 16"><path d="M8 2.5v8M4.5 7 8 10.5 11.5 7M3 13.5h10"/></svg>',
    finished: '<svg viewBox="0 0 16 16"><path d="M3 8.5l3 3 7-7"/></svg>',
    browser: '<svg viewBox="0 0 16 16"><rect x="2" y="3" width="12" height="10" rx="1"/><path d="M2 6h12"/></svg>',
    queue: '<svg viewBox="0 0 16 16"><path d="M2.5 4h11M2.5 8h11M2.5 12h7"/></svg>',
};

type View = {id: string, label: string, icon: string, sub?: boolean, gap?: boolean, match?: (d: Status) => boolean};
const VIEWS: View[] = [
    {id: 'all', label: 'All Downloads', icon: ICON.folder, match: () => true},
    ...Object.entries(TYPES).map(([t, {label}]) => ({id: t, label, icon: ICON.folder, sub: true, match: (d: Status) => typeOf(d.name) === t})),
    {id: 'unfinished', label: 'Unfinished', icon: ICON.unfinished, gap: true, match: (d: Status) => d.state !== 'done'},
    {id: 'finished', label: 'Finished', icon: ICON.finished, match: (d: Status) => d.state === 'done'},
    {id: 'queue', label: 'Queue', icon: ICON.queue, gap: true, match: (d: Status) => !!d.queue},
    {id: 'media', label: 'Sent from Browser', icon: ICON.browser, gap: true},
];
let view = VIEWS.find((v) => v.id === stored('view', 'all')) ?? VIEWS[0];

function renderSidebar() {
    const nav = $('sidebar');
    nav.replaceChildren(...VIEWS.flatMap((v) => {
        const item = el('div', 'item' + (v.sub ? ' sub' : '') + (v === view ? ' current' : ''), '');
        item.innerHTML = v.icon;
        item.append(el('span', '', v.label), el('span', 'count', String(v.match ? all.filter(v.match).length : mediaCount)));
        item.addEventListener('click', () => setView(v));
        return v.gap ? [el('div', 'gap', ''), item] : [item];
    }));
}

function setView(v: View) {
    view = v;
    store('view', v.id);
    $('table-wrap').hidden = v.id === 'media';
    $('media-view').hidden = v.id !== 'media';
    renderSidebar();
    renderTable();
}

// --- Speed, measured from two real readings of the bytes written ---

const samples = new Map<number, {done: number, at: number, speed: number}>();
const speeds = new Map<number, number>();

function measure(d: Status) {
    if (d.state !== 'downloading') {
        samples.delete(d.id);
        speeds.delete(d.id);
        return;
    }
    const now = performance.now();
    const prev = samples.get(d.id);
    if (!prev) {
        samples.set(d.id, {done: d.done, at: now, speed: -1});
        return;
    }
    if (now - prev.at < 900) return;
    const speed = (d.done - prev.done) / ((now - prev.at) / 1000);
    samples.set(d.id, {done: d.done, at: now, speed});
    speeds.set(d.id, speed);
}

// timeLeft is worked out from the measured speed only; none is shown until
// there is one.
function timeLeft(d: Status): number | null {
    const s = speeds.get(d.id);
    if (s === undefined || s <= 0 || d.size <= 0 || d.parts) return null;
    return (d.size - d.done) / s;
}

function progress(d: Status): number | null {
    if (d.state === 'done') return 1;
    if (d.parts) return (d.partsDone ?? 0) / d.parts;
    return d.size > 0 ? d.done / d.size : null;
}

function statusOf(d: Status): {text: string, cls: string, title: string} {
    const f = progress(d);
    const title = d.error ?? '';
    switch (d.state) {
    case 'done': return {text: 'Complete', cls: 'ok', title};
    case 'checking': return {text: 'Connecting…', cls: '', title};
    case 'joining': return {text: 'Joining video and audio…', cls: '', title};
    case 'waiting': return {text: d.error || 'Waiting to try again', cls: '', title};
    case 'downloading':
        if (d.parts) return {text: `Segment ${d.partsDone ?? 0} of ${d.parts}`, cls: '', title};
        return {text: f !== null ? percent(f) : `${size(d.done)} received`, cls: '', title};
    case 'paused':
        // Queued and not started yet; one with bytes in reads Paused, as any other.
        if (d.queue && !f) return {text: 'Queued', cls: '', title};
        return {text: f !== null ? `Paused at ${percent(f)}` : 'Paused', cls: '', title};
    case 'failed': return {text: `Error: ${d.error ?? 'failed'}`, cls: 'bad', title};
    case 'cancelled': return {text: d.error && d.error !== 'Cancelled.' ? d.error : 'Cancelled', cls: 'note', title};
    }
    return {text: d.state, cls: '', title};
}

// --- Columns ---

type Col = {id: string, label: string, width: number, num?: boolean, key: (d: Status) => string | number, fill: (td: HTMLTableCellElement, d: Status) => void};
const COLS: Col[] = [
    {
        id: 'name', label: 'File Name', width: 250, key: (d) => d.name.toLowerCase(),
        fill: (td, d) => {
            td.replaceChildren(el('span', 'ftype ' + typeOf(d.name), ''), d.name);
            td.title = d.path || d.name;
        },
    },
    {
        id: 'size', label: 'Size', width: 90, num: true, key: (d) => d.size >= 0 ? d.size : d.done,
        fill: (td, d) => { td.textContent = d.size >= 0 ? size(d.size) : d.done > 0 ? `${size(d.done)} so far` : ''; },
    },
    {
        id: 'status', label: 'Status', width: 180, key: (d) => (isRunning(d) ? 2 : d.state === 'done' ? 0 : 1) * 10 + (progress(d) ?? 0),
        fill: (td, d) => {
            const s = statusOf(d);
            const f = progress(d);
            let box = td.firstElementChild as HTMLElement | null;
            if (!box) {
                box = el('div', 'status', '');
                const bar = el('div', 'bar', '');
                bar.append(el('span', '', ''));
                box.append(bar, el('span', 'text', ''));
                td.append(box);
            }
            const bar = box.children[0] as HTMLElement;
            bar.hidden = f === null || d.state === 'done' || d.state === 'failed' || d.state === 'cancelled';
            (bar.firstElementChild as HTMLElement).style.width = `${(f ?? 0) * 100}%`;
            const text = box.children[1] as HTMLElement;
            text.textContent = s.text;
            text.className = 'text ' + s.cls;
            td.title = s.title || s.text;
        },
    },
    {
        id: 'left', label: 'Time Left', width: 90, num: true, key: (d) => timeLeft(d) ?? Infinity,
        fill: (td, d) => { const t = timeLeft(d); td.textContent = t === null ? '' : duration(t); },
    },
    {
        id: 'rate', label: 'Transfer Rate', width: 100, num: true, key: (d) => speeds.get(d.id) ?? -1,
        fill: (td, d) => { const s = speeds.get(d.id); td.textContent = s === undefined ? '' : `${size(Math.round(s))}/s`; },
    },
    {
        id: 'added', label: 'Date Added', width: 130, key: (d) => d.started || '',
        fill: (td, d) => {
            const t = d.started ? new Date(d.started) : null;
            td.textContent = t && !isNaN(t.getTime()) ? t.toLocaleString(undefined, {dateStyle: 'short', timeStyle: 'short'}) : '';
        },
    },
];

const widths: Record<string, number> = stored('widths', {});
let sort: {col: string, desc: boolean} = stored('sort', {col: 'added', desc: true});
let resizing = false;

function renderHead() {
    $('cols').replaceChildren(...COLS.map((c) => {
        const col = document.createElement('col');
        col.style.width = `${widths[c.id] ?? c.width}px`;
        return col;
    }));
    $('head').replaceChildren(...COLS.map((c, i) => {
        const th = el('th', c.num ? 'num' : '', c.label);
        if (sort.col === c.id) th.append(el('span', 'arrow', sort.desc ? '▼' : '▲'));
        th.addEventListener('click', () => {
            if (resizing) return;
            sort = sort.col === c.id ? {col: c.id, desc: !sort.desc} : {col: c.id, desc: c.id !== 'name'};
            store('sort', sort);
            renderHead();
            renderTable();
        });
        const grip = el('span', 'grip', '');
        grip.addEventListener('mousedown', (e) => startResize(e, c, i));
        grip.addEventListener('click', (e) => e.stopPropagation());
        th.append(grip);
        return th;
    }));
}

function startResize(e: MouseEvent, c: Col, i: number) {
    e.preventDefault();
    e.stopPropagation();
    resizing = true;
    const col = $('cols').children[i] as HTMLElement;
    const x0 = e.clientX, w0 = col.getBoundingClientRect().width;
    const move = (m: MouseEvent) => {
        widths[c.id] = Math.max(50, Math.round(w0 + m.clientX - x0));
        col.style.width = `${widths[c.id]}px`;
    };
    const up = () => {
        document.removeEventListener('mousemove', move);
        document.removeEventListener('mouseup', up);
        store('widths', widths);
        setTimeout(() => { resizing = false; }); // the click that ends a drag doesn't sort
    };
    document.addEventListener('mousemove', move);
    document.addEventListener('mouseup', up);
}

// --- The table ---

let all: Status[] = [];
let queue = {running: false, limit: 0}; // the queue and speed limiter
let shown: Status[] = [];
const rows = new Map<number, HTMLTableRowElement>();
const selected = new Set<number>();
let anchor: number | null = null;

function sorted(list: Status[]) {
    // The queue is shown in the order it runs.
    if (view.id === 'queue') return [...list].sort((a, b) => (a.queue ?? 0) - (b.queue ?? 0));
    const col = COLS.find((c) => c.id === sort.col) ?? COLS[COLS.length - 1];
    const dir = sort.desc ? -1 : 1;
    return [...list].sort((a, b) => {
        const x = col.key(a), y = col.key(b);
        if (x < y) return -dir;
        if (x > y) return dir;
        return (a.id - b.id) * dir;
    });
}

function renderTable() {
    if (!view.match) return;
    shown = sorted(all.filter(view.match));
    const body = $('rows');
    const keep = new Set(shown.map((d) => d.id));
    for (const [id, tr] of rows) {
        if (!keep.has(id)) {
            tr.remove();
            rows.delete(id);
        }
    }
    shown.forEach((d, i) => {
        let tr = rows.get(d.id);
        if (!tr) {
            tr = document.createElement('tr');
            tr.dataset.id = String(d.id);
            for (const c of COLS) tr.append(el('td', c.num ? 'num' : '', ''));
            rows.set(d.id, tr);
        }
        COLS.forEach((c, k) => c.fill(tr!.children[k] as HTMLTableCellElement, d));
        if (body.children[i] !== tr) body.insertBefore(tr, body.children[i] ?? null);
    });
    for (const id of selected) if (!all.some((d) => d.id === id)) selected.delete(id);
    paintSelection();
    const empty = $('empty');
    empty.hidden = shown.length > 0;
    empty.textContent = view.id === 'all'
        ? 'No downloads yet. Use Add URL, or the browser extension\'s right-click menu or "Send to app" button.'
        : 'Nothing here.';
}

function paintSelection() {
    for (const [id, tr] of rows) tr.classList.toggle('selected', selected.has(id));
    updateToolbar();
}

const pick = () => all.filter((d) => selected.has(d.id));

function selectRow(e: MouseEvent, id: number) {
    if (e.button === 2) {
        if (!selected.has(id)) {
            selected.clear();
            selected.add(id);
            anchor = id;
        }
    } else if (e.shiftKey && anchor !== null) {
        const ids = shown.map((d) => d.id);
        const a = ids.indexOf(anchor), b = ids.indexOf(id);
        if (!e.ctrlKey) selected.clear();
        if (a >= 0 && b >= 0) for (let i = Math.min(a, b); i <= Math.max(a, b); i++) selected.add(ids[i]);
    } else if (e.ctrlKey) {
        if (selected.has(id)) selected.delete(id); else selected.add(id);
        anchor = id;
    } else {
        selected.clear();
        selected.add(id);
        anchor = id;
    }
    paintSelection();
}

function rowId(e: Event): number | null {
    const tr = (e.target as HTMLElement).closest('tbody tr') as HTMLElement | null;
    return tr ? Number(tr.dataset.id) : null;
}

$('table-wrap').addEventListener('mousedown', (e) => {
    const id = rowId(e);
    if (id !== null) {
        selectRow(e, id);
    } else if (!(e.target as HTMLElement).closest('thead') && e.button === 0) {
        selected.clear();
        paintSelection();
    }
});
$('rows').addEventListener('dblclick', (e) => {
    const id = rowId(e);
    const d = all.find((x) => x.id === id);
    if (d) openDownload(d);
});
$('table-wrap').addEventListener('contextmenu', (e) => {
    e.preventDefault();
    if (rowId(e) !== null) openMenu(e.clientX, e.clientY);
});

// --- Actions ---

let noticeTimer = 0;
// notice shows a message in the status bar for a few seconds, in place of
// the totals.
function notice(text: string, cls = '') {
    const n = $('totals');
    n.textContent = text;
    n.className = cls;
    clearTimeout(noticeTimer);
    noticeTimer = window.setTimeout(() => { noticeTimer = 0; n.className = ''; showTotals(); }, 6000);
}

async function each(list: Status[], action: (id: number) => Promise<unknown>) {
    let failed = '';
    for (const d of list) {
        try { await action(d.id); } catch (err) { failed = `${d.name}: ${msg(err)}`; }
    }
    await refreshDownloads();
    if (failed) notice(failed, 'bad');
}

const resume = (list: Status[]) => each(list.filter((d) => d.resumable), ResumeDownload);
const stop = (list: Status[]) => each(list.filter(isRunning), PauseDownload);
const canQueue = (d: Status) => !d.queue && d.resumable;

async function queueCall(f: () => Promise<unknown>) {
    try {
        await f();
    } catch (err) {
        notice(msg(err), 'bad');
    }
    await refreshDownloads();
}
const toQueue = (list: Status[]) => queueCall(() => AddToQueue(list.filter(canQueue).map((d) => d.id)));
const offQueue = (list: Status[]) => queueCall(() => RemoveFromQueue(list.filter((d) => d.queue).map((d) => d.id)));
const move = (d: Status, by: number) => queueCall(() => MoveInQueue(d.id, by));

// openDownload opens a finished file, or the progress window of one that
// isn't finished, as double-click does in IDM.
function openDownload(d: Status) {
    if (d.state === 'done' && d.path) OpenFile(d.path).catch((err) => notice(msg(err), 'bad'));
    else showProgress(d);
}

function showProgress(d: Status) {
    ShowProgress(d.id).catch((err) => notice(msg(err), 'bad'));
}

function showFolder(d: Status) {
    if (d.path) ShowInFolder(d.path).catch((err) => notice(msg(err), 'bad'));
}

function ask(title: string, text: string, files: boolean, ok: string): Promise<{ok: boolean, files: boolean}> {
    const dlg = $<HTMLDialogElement>('confirm-dialog');
    $('confirm-title').textContent = title;
    $('confirm-text').textContent = text;
    $('confirm-files-label').hidden = !files;
    $<HTMLInputElement>('confirm-files').checked = false;
    $('confirm-ok').textContent = ok;
    dlg.returnValue = '';
    dlg.showModal();
    return new Promise((done) => dlg.addEventListener('close', () => {
        done({ok: dlg.returnValue === 'ok', files: files && $<HTMLInputElement>('confirm-files').checked});
    }, {once: true}));
}

async function remove(list: Status[]) {
    if (!list.length) return;
    if (list.some(isRunning)) {
        notice('Stop the running downloads before deleting them.', 'bad');
        return;
    }
    let text = list.length === 1 ? `Take "${list[0].name}" off the list?` : `Take ${list.length} downloads off the list?`;
    if (list.some((d) => d.resumable)) text += ' The unfinished files of paused downloads are deleted.';
    const a = await ask('Delete', text, list.some((d) => d.state === 'done'), 'Delete');
    if (!a.ok) return;
    try {
        await RemoveDownloads(list.map((d) => d.id), a.files);
    } catch (err) {
        notice(msg(err), 'bad');
    }
    await refreshDownloads();
}

async function removeCompleted() {
    const done = all.filter((d) => d.state === 'done');
    if (!done.length) return;
    const a = await ask('Delete Completed', `Take all ${done.length} finished downloads off the list? The files stay on disk.`, false, 'Delete');
    if (!a.ok) return;
    try {
        await RemoveDownloads(done.map((d) => d.id), false);
    } catch (err) {
        notice(msg(err), 'bad');
    }
    await refreshDownloads();
}

function updateToolbar() {
    const sel = pick();
    $<HTMLButtonElement>('tb-resume').disabled = !sel.some((d) => d.resumable);
    $<HTMLButtonElement>('tb-stop').disabled = !sel.some(isRunning);
    $<HTMLButtonElement>('tb-stop-all').disabled = !all.some(isRunning);
    $<HTMLButtonElement>('tb-delete').disabled = !sel.length || sel.some(isRunning);
    $<HTMLButtonElement>('tb-delete-done').disabled = !all.some((d) => d.state === 'done');
    $<HTMLButtonElement>('tb-queue-start').disabled = queue.running || !all.some((d) => d.queue);
    $<HTMLButtonElement>('tb-queue-stop').disabled = !queue.running;
}

$('tb-add').addEventListener('click', openAdd);
$('tb-resume').addEventListener('click', () => resume(pick()));
$('tb-stop').addEventListener('click', () => stop(pick()));
$('tb-stop-all').addEventListener('click', () => stop(all));
$('tb-delete').addEventListener('click', () => remove(pick()));
$('tb-delete-done').addEventListener('click', removeCompleted);
$('tb-options').addEventListener('click', () => ShowOptions('general').catch((e) => notice(msg(e), 'bad')));
$('tb-scheduler').addEventListener('click', () => ShowOptions('scheduler').catch((e) => notice(msg(e), 'bad')));
$('tb-queue-start').addEventListener('click', () => queueCall(StartQueue));
$('tb-queue-stop').addEventListener('click', () => queueCall(StopQueue));

// --- Right-click menu ---

const menu = $('menu');

function openMenu(x: number, y: number) {
    const sel = pick();
    if (!sel.length) return;
    const one = sel.length === 1 && sel[0].state === 'done' && sel[0].path ? sel[0] : null;
    const items: ([string, boolean, () => void] | null)[] = [
        ['Open', !!one, () => one && openDownload(one)],
        ['Progress…', sel.length === 1, () => showProgress(sel[0])],
        ['Open folder', !!one, () => one && showFolder(one)],
        null,
        ['Resume', sel.some((d) => d.resumable), () => resume(sel)],
        ['Stop', sel.some(isRunning), () => stop(sel)],
        null,
        ['Add to queue', sel.some(canQueue), () => toQueue(sel)],
        ['Remove from queue', sel.some((d) => d.queue), () => offQueue(sel)],
        ['Move up in queue', sel.length === 1 && !!sel[0].queue, () => move(sel[0], -1)],
        ['Move down in queue', sel.length === 1 && !!sel[0].queue, () => move(sel[0], 1)],
        null,
        ['Delete…', !sel.some(isRunning), () => remove(sel)],
    ];
    menu.replaceChildren(...items.map((it) => {
        if (!it) return document.createElement('hr');
        const b = el('button', '', it[0]) as HTMLButtonElement;
        b.disabled = !it[1];
        b.addEventListener('click', () => { hideMenu(); it[2](); });
        return b;
    }));
    menu.hidden = false;
    const r = menu.getBoundingClientRect();
    menu.style.left = `${Math.min(x, innerWidth - r.width - 4)}px`;
    menu.style.top = `${Math.min(y, innerHeight - r.height - 4)}px`;
}

const hideMenu = () => { menu.hidden = true; };
document.addEventListener('mousedown', (e) => { if (!menu.contains(e.target as Node)) hideMenu(); });
window.addEventListener('blur', hideMenu);
window.addEventListener('resize', hideMenu);
$('table-wrap').addEventListener('scroll', hideMenu);
document.addEventListener('contextmenu', (e) => {
    if (!(e.target as HTMLElement).closest('input, textarea')) e.preventDefault();
});

// --- Keys ---

document.addEventListener('keydown', (e) => {
    if (document.querySelector('dialog[open]') || (e.target as HTMLElement).closest('input, textarea')) return;
    if (e.key === 'Escape') {
        if (!menu.hidden) hideMenu(); else { selected.clear(); paintSelection(); }
        return;
    }
    if (view.id === 'media') return;
    if (e.key === 'Delete') {
        remove(pick());
    } else if (e.key === 'Enter') {
        const sel = pick();
        if (sel.length === 1) openDownload(sel[0]);
    } else if (e.key === 'a' && e.ctrlKey) {
        e.preventDefault();
        shown.forEach((d) => selected.add(d.id));
        paintSelection();
    } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        if (!shown.length) return;
        const at = shown.findIndex((d) => d.id === anchor);
        const next = shown[Math.max(0, Math.min(shown.length - 1, at < 0 ? 0 : at + (e.key === 'ArrowDown' ? 1 : -1)))];
        selected.clear();
        selected.add(next.id);
        anchor = next.id;
        paintSelection();
        rows.get(next.id)?.scrollIntoView({block: 'nearest'});
    }
});

// --- Add URL ---

const addDialog = $<HTMLDialogElement>('add-dialog');
const addInput = $<HTMLInputElement>('add-url');

async function openAdd() {
    addInput.value = '';
    $('add-error').textContent = '';
    try {
        const clip = (await ClipboardGetText()).trim();
        if (/^https?:\/\/\S+$/i.test(clip)) addInput.value = clip;
    } catch { /* an empty box is fine */ }
    addDialog.showModal();
    addInput.select();
}

$('add-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const ok = $<HTMLButtonElement>('add-ok');
    ok.disabled = true;
    try {
        const added = await AddURL(addInput.value);
        addDialog.close();
        // The Download File Info box takes it from here.
        if (added.asked) return;
        const id = added.id;
        if (view.id !== 'all' && view.id !== 'unfinished') setView(VIEWS[0]);
        selected.clear();
        selected.add(id);
        anchor = id;
        await refreshDownloads();
    } catch (err) {
        $('add-error').textContent = msg(err);
    } finally {
        ok.disabled = false;
    }
});
$('add-cancel').addEventListener('click', () => addDialog.close());

// --- Engine status and totals ---

const statusEl = $('engine-status');
const recheck = $<HTMLButtonElement>('recheck');
let engineOk = false;
let checking = false;
let lost = false; // the last list request failed

async function checkEngine() {
    if (checking) return;
    checking = true;
    recheck.disabled = true;
    try {
        const s = await CheckEngine();
        engineOk = s.running;
        statusEl.className = s.running ? 'ok' : 'bad';
        statusEl.textContent = s.running ? `● Engine running, version ${s.version}` : s.message;
    } catch (err) {
        engineOk = false;
        statusEl.className = 'bad';
        statusEl.textContent = `Couldn't check the engine: ${msg(err)}`;
    } finally {
        recheck.hidden = engineOk;
        recheck.disabled = false;
        checking = false;
    }
    await refreshMedia();
    await refreshDownloads();
}

function showTotals() {
    if (noticeTimer) return;
    const running = all.filter(isRunning).length;
    let total = 0;
    for (const s of speeds.values()) total += s;
    const parts = [`${all.length} ${all.length === 1 ? 'download' : 'downloads'}`];
    if (running) parts.push(`${running} running`);
    if (speeds.size) parts.push(`${size(Math.round(total))}/s`);
    if (queue.running) parts.push('queue running');
    if (queue.limit) parts.push(`speed limit ${size(queue.limit)}/s`);
    $('totals').textContent = parts.join(' · ');
}

async function refreshDownloads() {
    try {
        all = (await Downloads()) ?? [];
        queue = await QueueInfo();
    } catch {
        lost = true;
        if (engineOk) {
            engineOk = false;
            statusEl.className = 'bad';
            statusEl.textContent = 'The download engine isn\'t running.';
            recheck.hidden = false;
        }
        return;
    }
    if (lost) {
        lost = false;
        checkEngine();
    }
    all.forEach(measure);
    renderTable();
    renderSidebar();
    showTotals();
}

recheck.addEventListener('click', checkEngine);

// --- Streams sent from the browser ---

const LABEL: Record<string, string> = {hls: 'HLS', dash: 'DASH', video: 'Video', audio: 'Audio', file: 'File'};
const MODE: Record<string, string> = {
    multiplexed: 'parallel streams on one connection',
    ranges: 'several connections',
    single: 'one request',
};
// Server check results by item, kept here because the list redraws every few seconds.
const checks = new Map<number, {text: string, cls: string}>();
let mediaCount = 0;

async function refreshMedia() {
    let items;
    try {
        items = (await ReceivedMedia()) ?? [];
    } catch {
        return; // The status bar already says what's wrong.
    }
    mediaCount = items.length;
    $('media-empty').hidden = items.length > 0;
    $('media').replaceChildren(...items.map((m) => {
        const li = document.createElement('li');
        const title = el('div', 'title', '');
        title.append(el('span', 'kind', LABEL[m.kind] ?? m.kind), el('span', '', m.pageTitle || new URL(m.url).host));
        const details = [`from ${m.browser}`, new Date(m.received).toLocaleTimeString(), `${m.cookies} cookies`];
        if (m.manifestBytes) details.push(`playlist ${m.manifestBytes} bytes`);
        const check = document.createElement('button');
        check.textContent = 'Check server';
        check.addEventListener('click', () => checkServer(m.id, check));
        const dl = document.createElement('button');
        dl.textContent = 'Download';
        dl.addEventListener('click', () => startDownload(m.id, dl));
        li.append(title, el('div', 'url', m.url), el('div', 'meta', details.join(' · ')), check, ' ', dl);
        const result = checks.get(m.id);
        if (result) li.append(el('div', 'probe ' + result.cls, result.text));
        return li;
    }));
    renderSidebar();
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
        checks.set(id, {text: msg(err), cls: 'bad'});
    }
    await refreshMedia();
}

async function startDownload(id: number, btn: HTMLButtonElement) {
    btn.disabled = true;
    try {
        await Download(id);
        checks.delete(id);
        notice('Download started. It is in All Downloads.');
    } catch (err) {
        checks.set(id, {text: msg(err), cls: 'bad'});
        await refreshMedia();
    }
    btn.disabled = false;
    await refreshDownloads();
}

renderHead();
setView(view);
checkEngine();
setInterval(refreshMedia, 3000);
setInterval(refreshDownloads, 1000);
