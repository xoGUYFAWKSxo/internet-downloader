import './style.css';
import {AskID, ProgressID} from '../wailsjs/go/main/App';

// The same program is the main window and, started with --progress=ID or
// --ask=ID, one download's progress window or a link's Download File Info
// box.
Promise.all([ProgressID(), AskID()])
    .catch(() => [0, 0])
    .then(async ([progress, ask]) => {
        if (progress) (await import('./progress')).start(progress);
        else if (ask) (await import('./ask')).start(ask);
        else await import('./list');
    })
    .finally(() => document.body.classList.add('ready'));
