# gusset companion extension

The Firefox WebExtension half of [gusset](../README.md). It does two jobs:

- **Beacon courier.** It is the production `rendezvous.Signaling` carrier: the
  daemon hands it sealed beacons over the localhost WebSocket, the extension
  writes them to `storage.sync` (which Firefox Sync carries to the user's other
  devices, end-to-end encrypted), and it reports the peers' beacons back. The
  daemon can't touch the `storage.sync` API — only an extension can — so this is
  the courier the daemon proxies through. Only tiny sealed beacons ride
  `storage.sync`; never bulk data.
- **Status UI.** A popup that shows the live sync status the daemon streams over
  the same WebSocket, and a settings form to pair with the daemon.

## Pairing

Run the daemon with the WebSocket enabled and copy the token in:

```sh
gusset sync --ws 127.0.0.1:8765 --watch   # serves status + carries beacons
gusset ws-token                            # prints the pairing token
```

Paste the address and token into the extension popup. The token (derived from
the passphrase) is the access gate — localhost is not a trust boundary.

## Develop

```sh
npm install
npm run dev:firefox     # load into a dev Firefox with HMR
npm run build:firefox   # production build -> .output/firefox-mv3
npm run compile         # tsc typecheck
```

Stack: [WXT](https://wxt.dev) + React + Tailwind. Manifest V3, Firefox event-page
background (it owns the single daemon connection; the popup is too short-lived).
