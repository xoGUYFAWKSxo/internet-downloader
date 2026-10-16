# Privacy policy

Galactic Downloader (the Windows app and its browser extension for Chrome, Edge and Firefox) sends no data to its developer or to anyone else. It has no accounts, no analytics, no ads and no servers of its own. Last updated 2026-10-16.

## What the extension reads

- **The addresses of requests the browser makes** on the pages you visit, to spot video and audio files and HLS/DASH playlists. For each tab it keeps a list of at most 50 items found, with the request headers needed to download them again, in the browser's session storage: in memory only, cleared when the browser closes.
- **Playlists and the start of video files** it has spotted, fetched from the same site with the browser's cookies, as the page's own player does, to describe each item in its popup (for example "Video with sound · up to 1080p").
- **Files the browser starts downloading**: their address, the page that linked to them, their type and size. Files of 1 MB and larger, outside private windows, are handed to the app (see below) unless you turn that off in the popup. Smaller ones, and ones in private windows, are left alone. In the app's Options you can also raise the size and leave chosen sites and file types to the browser; those settings stay in the app's database on your computer.
- **The site's cookies for an item**, only when it is sent to the app.

## What it sends, and where

When you choose to download something (with the popup's buttons or the right-click menu), or the browser starts downloading a file of 1 MB or larger, the extension sends the item's address, the page it came from, the request headers it needs, the site's cookies for it, and for streams the playlist text, to the Galactic Downloader app **on the same computer**, through the browser's native messaging. It sends nothing anywhere else.

## What the app does with it

The app uses that information only to download the file from the site it came from. Cookies and sign-in headers are kept in memory, and while a download can still be resumed, in the app's database on your computer (`%LOCALAPPDATA%\InternetDownloader\downloads.db`, readable by your Windows account). They are wiped from it when the download ends, and never written to the app's log. A video stream the extension found (HLS, DASH, video or audio) is also kept in that database with its cookies and headers, so it is still offered after the app restarts; it is deleted once it is a day old (when the app starts or finds another stream), or sooner once 100 newer ones have come. The log names the sites downloaded from, but never their full addresses, which can hold sign-in tokens; Options, Log shows it, to copy into a bug report if you choose. The app connects only to the servers you download from, and once a day to GitHub to see whether a new version is out.

## Checking for a new version

Once a day the app asks GitHub (`api.github.com`) for the name of this project's latest release. The request carries the app's version in its User-Agent, and, as with any connection, GitHub sees your IP address; GitHub's privacy statement covers what it keeps. Nothing about your downloads is sent. If a newer version exists, the window and the tray menu offer the download page; nothing is downloaded or installed by itself. Turn the check off in Options, General.

## Files you download

Like a browser, the app marks each finished file as downloaded from the internet (the Windows "Zone.Identifier", or mark of the web), so Windows warns before running it. The mark holds the file's address and the page it came from, without anything after a `?`, and stays with the file on your computer.

## Removing the data

Closing the browser clears the extension's lists. Deleting a download in the app removes what it kept for it. Uninstalling the app leaves its download list in `%LOCALAPPDATA%\InternetDownloader`, which you can delete.

## Contact

Questions or problems: https://github.com/xoGUYFAWKSxo/internet-downloader/issues
