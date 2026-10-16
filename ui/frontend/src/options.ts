// The Options window: the engine's settings in one place, with the
// scheduler on a tab of its own, as IDM's Scheduler button opens.
import {ChooseFolder, EngineLog, SaveSettings, Settings, ShowLog} from '../wailsjs/go/main/App';
import {ClipboardSetText, Quit, WindowSetTitle} from '../wailsjs/runtime/runtime';
import {$, msg} from './util';

// The main window's categories, which can each have a folder.
const FOLDERS: [string, string][] = [['compressed', 'Compressed'], ['documents', 'Documents'], ['music', 'Music'], ['programs', 'Programs'], ['video', 'Video']];

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const ORDER = [1, 2, 3, 4, 5, 6, 0]; // Monday first

const PAGE = `
<form class="ask opt" id="opt-form">
    <div class="tabs" role="tablist">
        <button type="button" data-tab="general">General</button>
        <button type="button" data-tab="folders">Folders</button>
        <button type="button" data-tab="browser">Browser</button>
        <button type="button" data-tab="speed">Speed limiter</button>
        <button type="button" data-tab="queue">Queue</button>
        <button type="button" data-tab="scheduler">Scheduler</button>
        <button type="button" data-tab="log">Log</button>
    </div>
    <section data-panel="general">
        <label class="check"><input type="checkbox" id="opt-ask"> Show the Download File Info box for links from the browser and Add URL</label>
        <label class="check"><input type="checkbox" id="opt-done"> Show a box when a download finishes</label>
        <label class="check"><input type="checkbox" id="opt-awake"> Keep the computer awake while downloading</label>
        <label class="check"><input type="checkbox" id="opt-updates"> Check once a day for a new version of the app</label>
        <label class="check"><input type="checkbox" id="opt-clip"> When I switch to the window, offer to download links I copied</label>
        <label for="opt-dir">Download folder</label>
        <div class="ask-row"><input type="text" id="opt-dir" spellcheck="false"><button type="button" id="opt-browse">Browse…</button></div>
        <p class="note">The Download File Info box changes this to the folder you last saved into.</p>
    </section>
    <section data-panel="folders">
        <table class="pw-info opt-folders" id="opt-folders"></table>
        <p class="note">Files of these types are saved into their folder, made if it isn't there; empty uses the download folder. The Download File Info box offers it, and you can still pick another.</p>
    </section>
    <section data-panel="browser">
        <label class="check">Take the browser's downloads of <input type="number" id="opt-min" min="1" max="1000000" step="1"> MB and larger</label>
        <label for="opt-sites">Leave downloads from these sites to the browser (one per line)</label>
        <textarea id="opt-sites" rows="3" spellcheck="false" placeholder="example.com"></textarea>
        <label for="opt-types">Leave these file types to the browser</label>
        <input type="text" id="opt-types" spellcheck="false" placeholder="pdf mp3">
        <p class="note">A site covers its subdomains, and counts for the page you're on and the server the file comes from. Links you send with the right-click menu are always taken. The extension's own switch turns this off altogether.</p>
    </section>
    <section data-panel="speed">
        <label class="check"><input type="checkbox" id="opt-limit"> Limit download speed to
            <input type="number" id="opt-kb" min="1" max="1000000" step="1"> KB/s</label>
        <p class="note">The limit is shared by every download. The tray icon's menu turns it on and off.</p>
    </section>
    <section data-panel="queue">
        <label class="check">Download <input type="number" id="opt-max" min="1" max="10" step="1"> files at the same time from the queue</label>
        <label class="check">When the queue finishes
            <select id="opt-when">
                <option value="">do nothing</option>
                <option value="sleep">put the computer to sleep</option>
                <option value="hibernate">hibernate the computer</option>
                <option value="off">turn the computer off</option>
                <option value="restart">restart the computer</option>
            </select></label>
        <p class="note">Download Later puts a link in the queue, and "Add to queue" on the list's right-click menu adds a stopped download. Start Queue runs it in order; it stops when nothing is left. Sleep, hibernating, turning off or restarting waits a minute first, with a window to cancel it; it doesn't happen when you stop the queue yourself.</p>
    </section>
    <section data-panel="scheduler">
        <label class="check"><input type="checkbox" id="opt-sched"> Start the queue at <input type="time" id="opt-start"></label>
        <div class="days" id="opt-days"></div>
        <label class="check"><input type="checkbox" id="opt-stop-on"> Stop the queue at <input type="time" id="opt-stop"></label>
        <p class="note">This works while the app is running, with its icon in the tray; it doesn't wake the computer.</p>
    </section>
    <section data-panel="log">
        <textarea class="log" id="opt-log" readonly spellcheck="false"></textarea>
        <div class="ask-row">
            <button type="button" id="opt-log-copy">Copy</button>
            <button type="button" id="opt-log-refresh">Refresh</button>
            <button type="button" id="opt-log-folder">Show the file</button>
        </div>
        <p class="note">What the download engine did, newest at the bottom: useful to attach to a bug report. It has the names of servers but not full addresses, cookies or passwords.</p>
    </section>
    <p class="bad" id="opt-error"></p>
    <div class="pw-buttons">
        <span class="spacer"></span>
        <button type="submit" class="primary">OK</button>
        <button type="button" id="opt-cancel">Cancel</button>
    </div>
</form>`;

export function start(tab: string) {
    document.body.classList.add('progress-window');
    document.body.innerHTML = PAGE;
    const input = (id: string) => $<HTMLInputElement>(id);
    const err = $('opt-error');
    $('opt-days').replaceChildren(...ORDER.map((d) => {
        const l = document.createElement('label');
        l.className = 'check';
        l.innerHTML = `<input type="checkbox" value="${d}"> ${DAYS[d]}`;
        return l;
    }));
    const days = () => Array.from(document.querySelectorAll<HTMLInputElement>('#opt-days input'));
    $('opt-folders').replaceChildren(...FOLDERS.map(([key, label]) => {
        const tr = document.createElement('tr');
        tr.innerHTML = `<th><label for="opt-dir-${key}">${label}</label></th>
            <td><div class="ask-row"><input type="text" id="opt-dir-${key}" spellcheck="false" placeholder="The download folder"><button type="button" data-browse="${key}">Browse…</button></div></td>`;
        return tr;
    }));
    for (const b of Array.from(document.querySelectorAll<HTMLElement>('[data-browse]'))) {
        b.addEventListener('click', async () => {
            const f = input(`opt-dir-${b.dataset.browse}`);
            try {
                const d = await ChooseFolder(f.value || input('opt-dir').value);
                if (d) f.value = d;
            } catch (e) {
                err.textContent = msg(e);
            }
        });
    }

    const show = (t: string) => {
        for (const b of Array.from(document.querySelectorAll<HTMLElement>('[data-tab]'))) b.classList.toggle('current', b.dataset.tab === t);
        for (const p of Array.from(document.querySelectorAll<HTMLElement>('[data-panel]'))) p.hidden = p.dataset.panel !== t;
    };
    const log = $<HTMLTextAreaElement>('opt-log');
    const loadLog = async () => {
        try {
            log.value = await EngineLog();
        } catch (e) {
            log.value = msg(e);
        }
        log.scrollTop = log.scrollHeight;
    };
    let logLoaded = false;
    const shown = show;
    const showTab = (t: string) => {
        shown(t);
        if (t === 'log' && !logLoaded) {
            logLoaded = true;
            loadLog();
        }
    };
    for (const b of Array.from(document.querySelectorAll<HTMLElement>('[data-tab]'))) b.addEventListener('click', () => showTab(b.dataset.tab!));
    showTab(document.querySelector(`[data-tab="${tab}"]`) ? tab : 'general');
    $('opt-log-refresh').addEventListener('click', loadLog);
    $('opt-log-copy').addEventListener('click', async () => {
        const ok = await ClipboardSetText(log.value).catch(() => false);
        err.textContent = ok ? '' : "Couldn't copy the log.";
    });
    $('opt-log-folder').addEventListener('click', () => ShowLog().catch((e) => { err.textContent = msg(e); }));

    const sync = () => {
        input('opt-kb').disabled = !input('opt-limit').checked;
        input('opt-start').disabled = !input('opt-sched').checked;
        for (const d of days()) d.disabled = !input('opt-sched').checked;
        input('opt-stop-on').disabled = !input('opt-sched').checked;
        input('opt-stop').disabled = !input('opt-sched').checked || !input('opt-stop-on').checked;
    };
    for (const id of ['opt-limit', 'opt-sched', 'opt-stop-on']) input(id).addEventListener('change', sync);

    let loaded = false;
    let saved = '02:00';
    Settings().then((s) => {
        saved = s.schedStart;
        input('opt-ask').checked = s.ask === '1';
        input('opt-done').checked = s.done === '1';
        input('opt-awake').checked = s.awake === '1';
        input('opt-updates').checked = s.updates === '1';
        input('opt-clip').checked = s.clip === '1';
        input('opt-dir').value = s.dir;
        for (const [key] of FOLDERS) input(`opt-dir-${key}`).value = s[`dir.${key}`] ?? '';
        input('opt-limit').checked = s.limit === '1';
        input('opt-kb').value = s.limitKB;
        input('opt-min').value = s.catchMinMB;
        $<HTMLTextAreaElement>('opt-sites').value = s.catchSites;
        input('opt-types').value = s.catchTypes;
        input('opt-max').value = s.queueMax;
        $<HTMLSelectElement>('opt-when').value = s.whenDone ?? '';
        input('opt-sched').checked = s.sched === '1';
        input('opt-start').value = s.schedStart;
        input('opt-stop-on').checked = s.schedStop !== '';
        input('opt-stop').value = s.schedStop || '06:00';
        for (const d of days()) d.checked = s.schedDays.includes(d.value);
        loaded = true;
        sync();
    }).catch((e) => { err.textContent = msg(e); });

    $('opt-browse').addEventListener('click', async () => {
        try {
            const d = await ChooseFolder(input('opt-dir').value);
            if (d) input('opt-dir').value = d;
        } catch (e) {
            err.textContent = msg(e);
        }
    });
    $('opt-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        if (!loaded) return;
        const sched = input('opt-sched').checked;
        const picked = days().filter((d) => d.checked).map((d) => d.value).sort().join('');
        if (sched && !picked) {
            show('scheduler');
            err.textContent = 'Pick at least one day for the scheduler.';
            return;
        }
        const on = (b: boolean) => b ? '1' : '0';
        try {
            const folders: Record<string, string> = {};
            for (const [key] of FOLDERS) folders[`dir.${key}`] = input(`opt-dir-${key}`).value.trim();
            await SaveSettings({
                ...folders,
                ask: on(input('opt-ask').checked),
                done: on(input('opt-done').checked),
                awake: on(input('opt-awake').checked),
                updates: on(input('opt-updates').checked),
                clip: on(input('opt-clip').checked),
                dir: input('opt-dir').value.trim(),
                limit: on(input('opt-limit').checked),
                limitKB: input('opt-kb').value.trim(),
                catchMinMB: input('opt-min').value.trim(),
                catchSites: $<HTMLTextAreaElement>('opt-sites').value,
                catchTypes: input('opt-types').value,
                queueMax: input('opt-max').value.trim(),
                whenDone: $<HTMLSelectElement>('opt-when').value,
                sched: on(sched),
                schedStart: input('opt-start').value || saved,
                schedStop: input('opt-stop-on').checked ? input('opt-stop').value : '',
                schedDays: picked,
            });
            Quit();
        } catch (e) {
            err.textContent = msg(e);
        }
    });
    $('opt-cancel').addEventListener('click', () => Quit());
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') Quit(); });
    document.addEventListener('contextmenu', (e) => {
        if (!(e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement)) e.preventDefault();
    });
    WindowSetTitle('Options');
}
