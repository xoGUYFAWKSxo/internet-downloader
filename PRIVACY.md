# Privacy policy

Internet Downloader (the Windows app and its browser extension for Chrome, Edge and Firefox) sends no data to its developer or to anyone else. It has no accounts, no analytics, no ads and no servers of its own. Last updated 2026-10-16.

## What the extension reads

- **The addresses of requests the browser makes** on the pages you visit, to spot video and audio files and HLS/DASH playlists. For each tab it keeps a list of at most 50 items found, with the request headers needed to download them again, in the browser's session storage: in memory only, cleared when the browser closes.
- **Playlists and the start of video files** it has spotted, fetched from the same site with the browser's cookies, as the page's own player does, to describe each item in its popup (for example "Video with sound · up to 1080p").
- **The site's cookies for an item**, only when you choose to download it.

## What it sends, and where

When you choose to download something (with the popup's buttons or the right-click menu), the extension sends the item's address, the page it came from, the request headers it needs, the site's cookies for it, and for streams the playlist text, to the Internet Downloader app **on the same computer**, through the browser's native messaging. It sends nothing anywhere else.

## What the app does with it

The app uses that information only to download the file from the site it came from. Cookies and sign-in headers are kept in memory, and while a download can still be resumed, in the app's database on your computer (`%LOCALAPPDATA%\InternetDownloader\downloads.db`, readable by your Windows account). They are wiped from it when the download ends, and never written to the app's log. The app connects only to the servers you download from.

## Removing the data

Closing the browser clears the extension's lists. Deleting a download in the app removes what it kept for it. Uninstalling the app leaves its download list in `%LOCALAPPDATA%\InternetDownloader`, which you can delete.

## Contact

Questions or problems: https://github.com/xoGUYFAWKSxo/internet-downloader/issues
