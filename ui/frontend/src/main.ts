import './style.css';
import {AskID, OptionsTab, PowerWindow, ProgressID} from '../wailsjs/go/main/App';

// The same program is the main window and, started with --progress=ID,
// --ask=ID, --options or --power, one download's progress window, a link's
// Download File Info box, the Options window or the countdown before the
// computer sleeps or turns off.
Promise.all([ProgressID(), AskID(), OptionsTab(), PowerWindow()])
    .catch(() => [0, 0, '', false] as const)
    .then(async ([progress, ask, tab, power]) => {
        if (power) (await import('./power')).start();
        else if (progress) (await import('./progress')).start(progress);
        else if (ask) (await import('./ask')).start(ask);
        else if (tab) (await import('./options')).start(tab);
        else await import('./list');
    })
    .finally(() => document.body.classList.add('ready'));
