import './style.css';
import {AskID, OptionsTab, ProgressID} from '../wailsjs/go/main/App';

// The same program is the main window and, started with --progress=ID,
// --ask=ID or --options, one download's progress window, a link's Download
// File Info box or the Options window.
Promise.all([ProgressID(), AskID(), OptionsTab()])
    .catch(() => [0, 0, ''] as const)
    .then(async ([progress, ask, tab]) => {
        if (progress) (await import('./progress')).start(progress);
        else if (ask) (await import('./ask')).start(ask);
        else if (tab) (await import('./options')).start(tab);
        else await import('./list');
    })
    .finally(() => document.body.classList.add('ready'));
