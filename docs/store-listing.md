# Browser store listings

What to fill in on each store, and the steps. The extension does nothing without the Windows app, so every store gets the same note for reviewers.

## Files

From the release workflow's artifact (or `installer\build-release.ps1`):

| Store | Upload |
| --- | --- |
| Chrome Web Store | `store/GalacticDownloader-X.Y.Z-chrome-store.zip` (no `key`; the store refuses one) |
| Edge Add-ons | the same `chrome-store` zip |
| Firefox Add-ons | `GalacticDownloader-X.Y.Z-firefox-extension.zip` (plain JavaScript, so no source upload is needed) |

Icon: `extension/src/icon.png` (128 x 128). Privacy policy URL: https://github.com/xoGUYFAWKSxo/internet-downloader/blob/main/PRIVACY.md

## Listing text

**Name:** Galactic Downloader

**Summary** (from the manifest): Sends downloads and media streams to the Galactic Downloader app.

**Description:**

> Companion extension for Galactic Downloader, a free, open-source download manager for Windows 10 and 11. It needs the app, from https://github.com/xoGUYFAWKSxo/internet-downloader/releases
>
> - Spots video and audio files and HLS/DASH streams on the pages you visit, and lists them in its popup with a plain description, such as "Video with sound · up to 1080p".
> - Sends the one you choose to the app, with the cookies and headers the site needs, so downloads that need a sign-in work.
> - Adds "Download this with Galactic Downloader" to the right-click menu on videos.
>
> The app downloads over several connections at once, resumes after a dropped connection, and joins separate video and sound into one file. Copy-protected (DRM) streams are not supported.
>
> Nothing is sent to the developer or any server: the extension talks only to the app on your computer. Source code and privacy policy: https://github.com/xoGUYFAWKSxo/internet-downloader

**Category:** Chrome: Tools. Edge: Productivity. Firefox: Download Management.

**Screenshots** (Chrome needs at least one, 1280 x 800 or 640 x 400; Chrome also needs a 440 x 280 promo tile): the popup listing a found video on a page, the right-click menu, and the app's window.

## Privacy answers (Chrome Web Store "Privacy" tab; Edge asks similar)

**Single purpose:** Finding downloadable files and media streams on web pages and handing them to the Galactic Downloader app on the same computer.

**Permission justifications:**
- `nativeMessaging`: talks to the Galactic Downloader app on the user's computer, which does the downloading.
- `webRequest`: watches the requests a page makes to spot video and audio files and HLS/DASH playlists, which players load in the background and don't show as links.
- `cookies`: sends the site's cookies with a download the user chose, so files that need a sign-in can be downloaded by the app.
- `storage`: keeps each tab's list of found items in session storage (memory only), as Chrome stops idle service workers.
- `contextMenus`: the "Download this with Galactic Downloader" right-click items.
- `notifications`: says whether a right-click download was handed to the app.
- Host permission `<all_urls>`: media can come from any site, and from servers other than the page's own.

**Remote code:** No. All code is in the package.

**Data usage:** tick Authentication information (cookies), Web history (addresses of media requests) and Website content (playlists). Then certify all three statements: not sold, not used for unrelated purposes, not used for creditworthiness. All of it goes only to the app on the user's own computer.

Firefox: the manifest already declares `authenticationInfo`, `browsingActivity` and `websiteContent`, which Firefox shows when the extension is installed.

## Notes for reviewers (all three stores)

> This extension works with a native Windows app, which it reaches through native messaging. To test it:
> 1. On Windows 10 or 11, install the app from https://github.com/xoGUYFAWKSxo/internet-downloader/releases/latest (GalacticDownloader-X.Y.Z-setup.exe; no admin rights needed). It registers the native messaging host for Chrome, Edge and Firefox.
> 2. Click the extension's icon, then "Check connection to the app": it shows the app's version.
> 3. Open a page with an HLS test stream, for example https://hlsjs.video-dev.org/demo/, and play it. The popup lists the playlists; "Download" sends one to the app, which opens a box asking where to save it.
>
> Without the app, the popup says it can't reach the app and links to the download page.

## Steps

1. **Chrome Web Store:** sign up at https://chrome.google.com/webstore/devconsole (one-time $5 fee). New item, upload the `chrome-store` zip, fill in the listing and privacy tabs as above, then submit for review. A review usually takes days, but `<all_urls>`, `webRequest` and `cookies` can mean a longer one.
2. **Edge Add-ons:** sign up at https://partner.microsoft.com/dashboard/microsoftedge (free), then the same steps with the same zip.
3. **Firefox Add-ons:** sign in at https://addons.mozilla.org/developers/ (free). Submit a new add-on, choose "On this site" (listed), and upload the Firefox zip. Add-on URL: `galactic-downloader` (`internet-downloader` is taken).
4. **Once Chrome and Edge show the extension's ID** (on each store's dashboard, as 32 letters), the app must allow them: they go into `ChromeExtensionIds` in `installer/register-native-host.ps1`, and the next release's installer registers them. Until then, an extension installed from those stores can't reach the app. Firefox keeps the same ID (`galactic-downloader@xoguyfawksxo`), so it works at once.
