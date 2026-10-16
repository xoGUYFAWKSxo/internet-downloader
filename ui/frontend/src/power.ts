// The countdown when the queue has finished and Options says to put the
// computer to sleep, hibernate it, turn it off or restart it. Closing it
// cancels, like Cancel.
import {CancelPower, PowerInfo, PowerNow} from '../wailsjs/go/main/App';
import {Quit, WindowSetTitle} from '../wailsjs/runtime/runtime';
import {$, msg} from './util';

const ACTION: Record<string, {will: string, now: string, title: string}> = {
    sleep: {will: 'go to sleep', now: 'Sleep now', title: 'Going to sleep'},
    hibernate: {will: 'hibernate', now: 'Hibernate now', title: 'Hibernating'},
    off: {will: 'turn off', now: 'Turn off now', title: 'Turning off'},
    restart: {will: 'restart', now: 'Restart now', title: 'Restarting'},
};

const PAGE = `
<div class="pw power">
    <p class="pw-name">The queue has finished.</p>
    <p id="pow-text" aria-live="polite"></p>
    <p class="bad" id="pow-error"></p>
    <div class="pw-buttons">
        <span class="spacer"></span>
        <button id="pow-now"></button>
        <button id="pow-cancel" class="primary">Cancel</button>
    </div>
</div>`;

export function start() {
    document.body.innerHTML = PAGE;
    const err = $('pow-error');
    let failed = false;
    const tick = async () => {
        if (failed) return;
        try {
            const p = await PowerInfo();
            if (failed) return;
            if (!p.action) {
                Quit();
                return;
            }
            const t = ACTION[p.action] ?? ACTION.off;
            const n = p.left ?? 0;
            $('pow-text').textContent = `The computer will ${t.will} in ${n} second${n === 1 ? '' : 's'}.`;
            $('pow-now').textContent = t.now;
            WindowSetTitle(t.title);
        } catch (e) {
            err.textContent = msg(e);
        }
    };
    tick();
    setInterval(tick, 500);
    $('pow-cancel').addEventListener('click', () => CancelPower().finally(() => Quit()));
    $('pow-now').addEventListener('click', () => PowerNow().catch((e) => {
        failed = true;
        err.textContent = msg(e);
        $('pow-text').textContent = '';
        $<HTMLButtonElement>('pow-now').hidden = true;
        $('pow-cancel').textContent = 'Close';
    }));
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') CancelPower().finally(() => Quit()); });
    $('pow-cancel').focus();
}
